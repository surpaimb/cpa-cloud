// Independently authored public synthetic tests; see PROVENANCE.md.
package masterkeyrotationmodel_test

import (
	"testing"

	m "cpacloud.local/server/internal/masterkeyrotationmodel"
)

// Separate handwritten oracle: fixture/policy expectations do not use Catalog.
type expectation struct {
	id     string
	action m.Action
	domain string
}

var expected = []expectation{
	{"domain-digest", m.FreshDomain, ""},
	{"domain-aead", m.FreshDomain, ""},
	{"domain-response-wrap", m.FreshDomain, ""},
	{"domain-response-fingerprint", m.FreshDomain, ""},
	{"employee-key", m.Reissue, "domain-digest"},
	{"admin-session", m.Retire, ""},
	{"self-session", m.Retire, ""},
	{"enrollment", m.Retire, ""},
	{"oauth-state", m.Retire, ""},
	{"audit-cursor", m.Retire, ""},
	{"governance-cursor", m.Retire, ""},
	{"request-cursor", m.Retire, ""},
	{"key-request-cursor", m.Retire, ""},
	{"pool-session", m.Retire, ""},
	{"proxy-operation", m.Retire, ""},
	{"batch-input", m.Retire, ""},
	{"batch-operation", m.Retire, ""},
	{"redemption-code", m.Retire, ""},
	{"upstream-api", m.Reencrypt, "domain-aead"},
	{"gemini-api", m.Reencrypt, "domain-aead"},
	{"codex-auth", m.Reencrypt, "domain-aead"},
	{"oauth-secret", m.Retire, ""},
	{"proxy-credential", m.Reencrypt, "domain-aead"},
	{"webhook-secret", m.Reencrypt, "domain-aead"},
	{"wallet-cursor", m.Retire, ""},
	{"classification-cursor", m.Retire, ""},
	{"subscription-cursor", m.Retire, ""},
	{"catalog-cursor", m.Retire, ""},
	{"purchase-quote", m.Retire, ""},
	{"renewal-quote", m.Retire, ""},
	{"redemption-cursor", m.Retire, ""},
	{"adjustment-cursor", m.Retire, ""},
	{"topup-cursor", m.Retire, ""},
	{"response-dek", m.Retire, ""},
	{"response-wrap", m.Retire, ""},
	{"response-state", m.Retire, ""},
	{"response-operation", m.Retire, ""},
	{"response-background", m.Retire, ""},
	{"response-managed-tool", m.Retire, ""},
	{"installation-canary", m.RegenerateBinding, ""},
	{"binding-seed", m.RegenerateBinding, ""},
	{"legacy-root-file", m.Retire, ""},
	{"portable-binding", m.Retire, ""},
	{"backup-v1", m.Retire, ""},
	{"backup-v2", m.Retire, ""},
	{"backup-v3", m.Retire, ""},
	{"backup-generation", m.Retire, ""},
	{"independent-w", m.KeepIndependent, ""},
	{"bcrypt-password", m.KeepIndependent, ""},
	{"unkeyed-ledger", m.KeepIndependent, ""},
	{"independent-checksum", m.KeepIndependent, ""},
	{"pkce", m.Retire, ""},
	{"independent-transient", m.Retire, ""},
}

func isOperation(id string) bool {
	switch id {
	case "proxy-operation", "batch-input", "batch-operation", "response-operation", "response-background", "response-managed-tool", "backup-generation":
		return true
	}
	return false
}

func fixture() (m.Input, m.Proof) {
	in := m.Input{
		Purpose: m.Purpose, Rotation: 10, Instance: 11,
		Source: m.Root{Value: 1, Epoch: 7, Origin: m.Independent},
		Target: m.Root{Value: 2, Epoch: 8, Origin: m.Independent}, MinimumEpoch: 7,
		RetiredRotations: []m.Symbol{8, 9}, SnapshotComplete: true, Quiescent: true, ObligationsResolved: true,
		OldKeys: []m.OldKey{{ID: 500, Plaintext: 501, Digest: 502}, {ID: 503, Plaintext: 504, Digest: 505}},
		NewKeys: []m.NewKey{
			{OldID: 500, ID: 600, Plaintext: 601, Digest: 602, Root: 2, DigestKey: 1000, Epoch: 8, Origin: m.Independent, DigestOrigin: m.DerivedFromTarget},
			{OldID: 503, ID: 603, Plaintext: 604, Digest: 605, Root: 2, DigestKey: 1000, Epoch: 8, Origin: m.Independent, DigestOrigin: m.DerivedFromTarget},
		},
	}
	p := m.Proof{Purpose: m.Purpose, Rotation: 10, Instance: 11, SourceRoot: 1, TargetRoot: 2, SourceEpoch: 7, TargetEpoch: 8, Binding: 12}
	for i, e := range expected {
		count := uint32(1)
		if e.id == "employee-key" {
			count = 2
		}
		if e.id == "legacy-root-file" {
			count = 0
		}
		before := m.Symbol(100 + i)
		if count == 0 {
			before = 0
		}
		in.Families = append(in.Families, m.Family{ID: e.id, Format: "source605/" + e.id, Epoch: 7, Count: count, Before: before, Complete: true})
		r := m.Receipt{ID: e.id, Purpose: "rotation/v1/" + e.id, Format: "source605/" + e.id, Action: e.action, Count: count, Before: before, Epoch: 8, Complete: true}
		switch e.action {
		case m.FreshDomain, m.Reissue, m.Reencrypt, m.RegenerateBinding:
			r.After, r.Root, r.Key, r.Binding = m.Symbol(1000+i), 2, 2, 12
			r.Origin, r.Authenticated = m.DerivedFromTarget, true
			if e.domain == "domain-digest" {
				r.Key = 1000
			}
			if e.domain == "domain-aead" {
				r.Key = 1001
			}
		case m.KeepIndependent:
			r.After, r.Origin = r.Before, m.Independent
		}
		p.Receipts = append(p.Receipts, r)
		if isOperation(e.id) {
			id := m.Symbol(2000 + i)
			outcome := m.Completed
			if e.id == "response-background" {
				outcome = m.Uncertain
			}
			in.Operations = append(in.Operations, m.Operation{ID: id, Consumer: e.id, Epoch: 7, Outcome: outcome})
			p.Fences = append(p.Fences, m.OperationFence{ID: id, Outcome: outcome, Retired: true, FactsPreserved: true})
		}
	}
	return in, p
}

func family(in *m.Input, id string) *m.Family {
	for i := range in.Families {
		if in.Families[i].ID == id {
			return &in.Families[i]
		}
	}
	panic("fixture family absent")
}
func receipt(p *m.Proof, id string) *m.Receipt {
	for i := range p.Receipts {
		if p.Receipts[i].ID == id {
			return &p.Receipts[i]
		}
	}
	panic("fixture receipt absent")
}
func requireCode(t *testing.T, got, want m.Code) {
	t.Helper()
	if got != want {
		t.Fatalf("fixed result: got %s, want %s", got, want)
	}
}
func prepare(t *testing.T, in m.Input) *m.Machine {
	t.Helper()
	machine, code := m.Prepare(in)
	requireCode(t, code, m.OK)
	if machine == nil || machine.Phase() != m.Prepared || machine.CanServe() {
		t.Fatal("prepare phase")
	}
	return machine
}

func TestClosedCatalogIndependentOracle(t *testing.T) {
	got := m.Catalog()
	if len(got) != 53 || len(got) != len(expected) {
		t.Fatal("catalog coverage")
	}
	for i, e := range expected {
		s := got[i]
		if s.ID != e.id || s.Action != e.action || s.Domain != e.domain || s.Purpose != "rotation/v1/"+e.id || s.Format != "source605/"+e.id || s.Operations != isOperation(e.id) {
			t.Fatal("catalog policy")
		}
		one := i < 4 || e.id == "installation-canary" || e.id == "binding-seed" || e.id == "independent-w"
		if s.One != one || s.EmptyOnly != (e.id == "legacy-root-file") {
			t.Fatal("catalog cardinality")
		}
	}
	got[0].Action = m.KeepIndependent
	if m.Catalog()[0].Action != m.FreshDomain {
		t.Fatal("catalog alias")
	}
}

func TestCommitRevokesOldAndAdmitsOnlyDeclaredFreshKeys(t *testing.T) {
	in, p := fixture()
	machine := prepare(t, in)
	requireCode(t, machine.AdmitEmployeeKey(8, 600, 601), m.Denied)
	requireCode(t, machine.Verify(p), m.OK)
	if machine.CanServe() {
		t.Fatal("verify enabled serving")
	}
	requireCode(t, machine.Commit(), m.OK)
	if !machine.CanServe() || machine.Phase() != m.Committed {
		t.Fatal("commit phase")
	}
	for _, k := range in.NewKeys {
		requireCode(t, machine.AdmitEmployeeKey(8, k.ID, k.Plaintext), m.OK)
		requireCode(t, machine.AdmitEmployeeKey(7, k.ID, k.Plaintext), m.Denied)
		requireCode(t, machine.AdmitEmployeeKey(8, k.ID, 99999), m.Denied)
	}
	for _, k := range in.OldKeys {
		for _, epoch := range []m.Epoch{7, 8} {
			requireCode(t, machine.AdmitEmployeeKey(epoch, k.ID, k.Plaintext), m.Denied)
		}
	}
	for _, f := range in.Families {
		requireCode(t, machine.AdmitRetiredState(7, f.Before), m.Denied)
	}
	for _, op := range in.Operations {
		requireCode(t, machine.AdmitOperation(op.ID), m.Denied)
	}
	requireCode(t, machine.AdmitOperation(99999), m.Denied)
	for _, epoch := range []m.Epoch{0, 7, 8, 9} {
		requireCode(t, machine.AdmitRestore(epoch, 1), m.Denied)
		requireCode(t, machine.AdmitRestore(epoch, 2), m.Denied)
	}
	requireCode(t, machine.Commit(), m.InvalidTransition)
	requireCode(t, machine.Abort(), m.InvalidTransition)
	requireCode(t, machine.Verify(p), m.InvalidTransition)
}

func TestAbortAndZeroValuesNeverServe(t *testing.T) {
	var nilMachine *m.Machine
	for _, machine := range []*m.Machine{nilMachine, new(m.Machine)} {
		requireCode(t, machine.Commit(), m.InvalidTransition)
		requireCode(t, machine.Abort(), m.InvalidTransition)
		requireCode(t, machine.Verify(m.Proof{}), m.InvalidTransition)
		requireCode(t, machine.AdmitEmployeeKey(8, 600, 601), m.Denied)
		if machine.CanServe() || machine.Phase() != m.Unprepared {
			t.Fatal("zero machine serving")
		}
	}
	for _, verify := range []bool{false, true} {
		in, p := fixture()
		machine := prepare(t, in)
		requireCode(t, machine.Commit(), m.InvalidTransition)
		if verify {
			requireCode(t, machine.Verify(p), m.OK)
		}
		requireCode(t, machine.Abort(), m.OK)
		if machine.Phase() != m.Aborted || machine.CanServe() {
			t.Fatal("abort serving")
		}
		requireCode(t, machine.AdmitEmployeeKey(8, 600, 601), m.Denied)
		requireCode(t, machine.Commit(), m.InvalidTransition)
		requireCode(t, machine.Verify(p), m.InvalidTransition)
		requireCode(t, machine.Abort(), m.InvalidTransition)
	}
}

func TestPrepareRefusals(t *testing.T) {
	cases := []struct {
		name   string
		change func(*m.Input)
		code   m.Code
	}{
		{"purpose", func(in *m.Input) { in.Purpose = "rewrap-only" }, m.InvalidPlan},
		{"rotation", func(in *m.Input) { in.Rotation = 0 }, m.InvalidPlan},
		{"instance", func(in *m.Input) { in.Instance = 0 }, m.InvalidPlan},
		{"incomplete", func(in *m.Input) { in.SnapshotComplete = false }, m.InvalidPlan},
		{"inflight", func(in *m.Input) { in.Quiescent = false }, m.InvalidPlan},
		{"funded-obligation", func(in *m.Input) { in.ObligationsResolved = false }, m.InvalidPlan},
		{"no-floor", func(in *m.Input) { in.MinimumEpoch = 0 }, m.InvalidPlan},
		{"old-backup-floor", func(in *m.Input) { in.MinimumEpoch = 8 }, m.InvalidPlan},
		{"rotation-replay", func(in *m.Input) { in.RetiredRotations = append(in.RetiredRotations, 10) }, m.InvalidPlan},
		{"history-duplicate", func(in *m.Input) { in.RetiredRotations = []m.Symbol{8, 8} }, m.InvalidPlan},
		{"history-zero", func(in *m.Input) { in.RetiredRotations[0] = 0 }, m.InvalidPlan},
		{"history-limit", func(in *m.Input) { in.RetiredRotations = make([]m.Symbol, 4097) }, m.InvalidPlan},
		{"same-root", func(in *m.Input) { in.Target.Value = 1 }, m.InvalidRoot},
		{"zero-target", func(in *m.Input) { in.Target.Value = 0 }, m.InvalidRoot},
		{"zero-source", func(in *m.Input) { in.Source.Value = 0 }, m.InvalidRoot},
		{"derived-root", func(in *m.Input) { in.Target.Origin = m.DerivedFromSource }, m.InvalidRoot},
		{"unknown-root", func(in *m.Input) { in.Target.Origin = m.UnknownOrigin }, m.InvalidRoot},
		{"unknown-source", func(in *m.Input) { in.Source.Origin = m.UnknownOrigin }, m.InvalidRoot},
		{"root-old-domain", func(in *m.Input) { in.Target.Value = in.Families[0].Before }, m.InvalidRoot},
		{"same-epoch", func(in *m.Input) { in.Target.Epoch = 7 }, m.InvalidRoot},
		{"epoch-gap", func(in *m.Input) { in.Target.Epoch = 9 }, m.InvalidRoot},
		{"epoch-wrap", func(in *m.Input) { in.Source.Epoch = ^m.Epoch(0); in.Target.Epoch = 0 }, m.InvalidRoot},
		{"missing-family", func(in *m.Input) { in.Families = in.Families[1:] }, m.InvalidCatalog},
		{"extra-family", func(in *m.Input) { in.Families = append(in.Families, in.Families[0]) }, m.InvalidCatalog},
		{"duplicate-family", func(in *m.Input) { in.Families[1] = in.Families[0] }, m.InvalidCatalog},
		{"unknown-family", func(in *m.Input) { in.Families[0].ID = "future-feature" }, m.InvalidCatalog},
		{"unknown-format", func(in *m.Input) { in.Families[0].Format = "unknown" }, m.InvalidCatalog},
		{"family-incomplete", func(in *m.Input) { in.Families[0].Complete = false }, m.InvalidCatalog},
		{"mixed-epoch", func(in *m.Input) { in.Families[1].Epoch = 8 }, m.InvalidCatalog},
		{"record-limit", func(in *m.Input) { in.Families[5].Count = 4097 }, m.InvalidCatalog},
		{"missing-domain", func(in *m.Input) { in.Families[0].Count = 0; in.Families[0].Before = 0 }, m.InvalidCatalog},
		{"empty-with-material", func(in *m.Input) { family(in, "legacy-root-file").Before = 800 }, m.InvalidCatalog},
		{"active-legacy-root", func(in *m.Input) { f := family(in, "legacy-root-file"); f.Count = 1; f.Before = 800 }, m.InvalidCatalog},
		{"source-alias", func(in *m.Input) { in.Families[1].Before = in.Families[0].Before }, m.InvalidCatalog},
		{"missing-source-material", func(in *m.Input) { in.Families[1].Before = 0 }, m.InvalidCatalog},
		{"old-key-count", func(in *m.Input) { in.OldKeys = in.OldKeys[:1] }, m.InvalidKeys},
		{"old-key-alias", func(in *m.Input) { in.OldKeys[1].Digest = in.OldKeys[0].Digest }, m.InvalidKeys},
		{"missing-reissue", func(in *m.Input) { in.NewKeys = in.NewKeys[:1] }, m.InvalidKeys},
		{"extra-reissue", func(in *m.Input) { in.NewKeys = append(in.NewKeys, in.NewKeys[0]) }, m.InvalidKeys},
		{"duplicate-reissue", func(in *m.Input) { in.NewKeys[1].OldID = 500 }, m.InvalidKeys},
		{"unknown-reissue", func(in *m.Input) { in.NewKeys[1].OldID = 999 }, m.InvalidKeys},
		{"old-key-id", func(in *m.Input) { in.NewKeys[0].ID = 500 }, m.InvalidKeys},
		{"old-plaintext", func(in *m.Input) { in.NewKeys[0].Plaintext = 501 }, m.InvalidKeys},
		{"old-digest", func(in *m.Input) { in.NewKeys[0].Digest = 502 }, m.InvalidKeys},
		{"transformed-key", func(in *m.Input) { in.NewKeys[0].Origin = m.DerivedFromSource }, m.InvalidKeys},
		{"wrong-digest-origin", func(in *m.Input) { in.NewKeys[0].DigestOrigin = m.Independent }, m.InvalidKeys},
		{"zero-digest-key", func(in *m.Input) { in.NewKeys[0].DigestKey = 0 }, m.InvalidKeys},
		{"wrong-key-root", func(in *m.Input) { in.NewKeys[0].Root = 1 }, m.InvalidKeys},
		{"wrong-key-epoch", func(in *m.Input) { in.NewKeys[0].Epoch = 7 }, m.InvalidKeys},
		{"new-key-alias", func(in *m.Input) { in.NewKeys[1].Plaintext = 601 }, m.InvalidKeys},
		{"missing-operation", func(in *m.Input) { in.Operations = in.Operations[1:] }, m.InvalidOperations},
		{"extra-operation", func(in *m.Input) { in.Operations = append(in.Operations, in.Operations[0]) }, m.InvalidOperations},
		{"unknown-outcome", func(in *m.Input) { in.Operations[0].Outcome = m.UnknownOutcome }, m.InvalidOperations},
		{"unknown-operation-family", func(in *m.Input) { in.Operations[0].Consumer = "admin-session" }, m.InvalidOperations},
		{"wrong-operation-epoch", func(in *m.Input) { in.Operations[0].Epoch = 8 }, m.InvalidOperations},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, _ := fixture()
			c.change(&in)
			machine, code := m.Prepare(in)
			requireCode(t, code, c.code)
			if machine != nil {
				t.Fatal("refused plan returned machine")
			}
		})
	}
}

func TestVerifyRefusalsAreAtomic(t *testing.T) {
	cases := []struct {
		name   string
		change func(*m.Proof)
	}{
		{"purpose", func(p *m.Proof) { p.Purpose = "rewrap-only" }},
		{"rotation", func(p *m.Proof) { p.Rotation = 9 }},
		{"instance", func(p *m.Proof) { p.Instance = 99 }},
		{"source-root", func(p *m.Proof) { p.SourceRoot = 2 }},
		{"target-root", func(p *m.Proof) { p.TargetRoot = 1 }},
		{"source-epoch", func(p *m.Proof) { p.SourceEpoch = 6 }},
		{"target-epoch", func(p *m.Proof) { p.TargetEpoch = 7 }},
		{"zero-binding", func(p *m.Proof) { p.Binding = 0 }},
		{"old-binding", func(p *m.Proof) { p.Binding = 140 }},
		{"root-binding", func(p *m.Proof) { p.Binding = 2 }},
		{"missing-receipt", func(p *m.Proof) { p.Receipts = p.Receipts[1:] }},
		{"extra-receipt", func(p *m.Proof) { p.Receipts = append(p.Receipts, p.Receipts[0]) }},
		{"duplicate-receipt", func(p *m.Proof) { p.Receipts[1] = p.Receipts[0] }},
		{"unknown-receipt", func(p *m.Proof) { p.Receipts[1].ID = "future" }},
		{"wrong-purpose", func(p *m.Proof) { p.Receipts[1].Purpose = "other" }},
		{"unknown-format", func(p *m.Proof) { p.Receipts[1].Format = "future" }},
		{"wrong-count", func(p *m.Proof) { p.Receipts[1].Count++ }},
		{"wrong-before", func(p *m.Proof) { p.Receipts[1].Before = 99 }},
		{"unresolved", func(p *m.Proof) { p.Receipts[1].Unresolved = 1 }},
		{"incomplete", func(p *m.Proof) { p.Receipts[1].Complete = false }},
		{"mixed-generation", func(p *m.Proof) { p.Receipts[1].Epoch = 7 }},
		{"wrong-root", func(p *m.Proof) { p.Receipts[1].Root = 1 }},
		{"old-domain", func(p *m.Proof) { p.Receipts[1].After = p.Receipts[1].Before }},
		{"root-as-domain", func(p *m.Proof) { p.Receipts[1].After = 2 }},
		{"duplicate-domain", func(p *m.Proof) { p.Receipts[1].After = p.Receipts[0].After }},
		{"wrong-domain-parent", func(p *m.Proof) { p.Receipts[1].Key = 1 }},
		{"independent-not-derived", func(p *m.Proof) { p.Receipts[1].Origin = m.Independent }},
		{"wrong-binding", func(p *m.Proof) { p.Receipts[1].Binding = 13 }},
		{"unauthenticated", func(p *m.Proof) { p.Receipts[1].Authenticated = false }},
		{"reencrypt-old-ciphertext", func(p *m.Proof) { r := receipt(p, "upstream-api"); r.After = r.Before }},
		{"reencrypt-old-key", func(p *m.Proof) { receipt(p, "upstream-api").Key = 101 }},
		{"reencrypt-skip", func(p *m.Proof) { receipt(p, "upstream-api").Action = m.KeepIndependent }},
		{"key-reuse-domain", func(p *m.Proof) { receipt(p, "employee-key").Key = 1001 }},
		{"key-digest-undeclared-domain", func(p *m.Proof) { p.Receipts[0].After = 10000; receipt(p, "employee-key").Key = 10000 }},
		{"canary-root", func(p *m.Proof) { receipt(p, "installation-canary").Root = 1 }},
		{"canary-key", func(p *m.Proof) { receipt(p, "installation-canary").Key = 1001 }},
		{"canary-old", func(p *m.Proof) { r := receipt(p, "installation-canary"); r.After = r.Before }},
		{"seed-binding", func(p *m.Proof) { receipt(p, "binding-seed").Binding = 13 }},
		{"seed-purpose", func(p *m.Proof) { receipt(p, "binding-seed").Purpose = "canary" }},
		{"seed-epoch", func(p *m.Proof) { receipt(p, "binding-seed").Epoch = 7 }},
		{"preserved-old-session", func(p *m.Proof) { r := receipt(p, "self-session"); r.After = r.Before }},
		{"rewrap-only-response", func(p *m.Proof) { receipt(p, "response-wrap").Action = m.Reencrypt }},
		{"preserved-response-dek", func(p *m.Proof) { r := receipt(p, "response-dek"); r.After = r.Before }},
		{"kept-backup-authority", func(p *m.Proof) { receipt(p, "backup-v3").Root = 1 }},
		{"empty-output", func(p *m.Proof) { receipt(p, "legacy-root-file").After = 999 }},
		{"empty-origin", func(p *m.Proof) { receipt(p, "legacy-root-file").Origin = m.Independent }},
		{"blind-w-rotation", func(p *m.Proof) { receipt(p, "independent-w").After = 5000 }},
		{"bcrypt-root-binding", func(p *m.Proof) { receipt(p, "bcrypt-password").Root = 2 }},
		{"missing-fence", func(p *m.Proof) { p.Fences = p.Fences[1:] }},
		{"extra-fence", func(p *m.Proof) { p.Fences = append(p.Fences, p.Fences[0]) }},
		{"duplicate-fence", func(p *m.Proof) { p.Fences[1] = p.Fences[0] }},
		{"unknown-fence", func(p *m.Proof) { p.Fences[0].ID = 999 }},
		{"wrong-outcome", func(p *m.Proof) { p.Fences[0].Outcome = m.Uncertain }},
		{"old-success-replay", func(p *m.Proof) { p.Fences[0].Retired = false }},
		{"discarded-facts", func(p *m.Proof) { p.Fences[0].FactsPreserved = false }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, p := fixture()
			machine := prepare(t, in)
			c.change(&p)
			requireCode(t, machine.Verify(p), m.InvalidProof)
			if machine.Phase() != m.Prepared || machine.CanServe() {
				t.Fatal("failed verify advanced")
			}
			requireCode(t, machine.Commit(), m.InvalidTransition)
			_, valid := fixture()
			requireCode(t, machine.Verify(valid), m.OK)
			requireCode(t, machine.Commit(), m.OK)
		})
	}
}

func TestEveryFamilyRejectsUnresolvedAndWrongDisposition(t *testing.T) {
	for _, e := range expected {
		t.Run(e.id, func(t *testing.T) {
			for _, kind := range []string{"unresolved", "action", "format", "purpose", "epoch"} {
				in, p := fixture()
				machine := prepare(t, in)
				r := receipt(&p, e.id)
				switch kind {
				case "unresolved":
					r.Unresolved = 1
				case "action":
					r.Action = m.UnknownAction
				case "format":
					r.Format = "unknown"
				case "purpose":
					r.Purpose = "other"
				case "epoch":
					r.Epoch = 7
				}
				requireCode(t, machine.Verify(p), m.InvalidProof)
			}
		})
	}
}

func TestKnownEmptySetsMustBeExplicit(t *testing.T) {
	in, p := fixture()
	in.OldKeys = nil
	in.NewKeys = nil
	in.Operations = nil
	p.Fences = nil
	for i, e := range expected {
		if e.action == m.FreshDomain || e.action == m.RegenerateBinding || e.id == "independent-w" {
			continue
		}
		in.Families[i].Count = 0
		in.Families[i].Before = 0
		p.Receipts[i] = m.Receipt{ID: e.id, Purpose: "rotation/v1/" + e.id, Format: "source605/" + e.id, Action: e.action, Epoch: 8, Complete: true}
	}
	machine := prepare(t, in)
	requireCode(t, machine.Verify(p), m.OK)
	requireCode(t, machine.Commit(), m.OK)
	requireCode(t, machine.AdmitEmployeeKey(8, 600, 601), m.Denied)
}

func TestDefensiveCopiesAndNoRetainedProofAliases(t *testing.T) {
	in, p := fixture()
	machine := prepare(t, in)
	in.Families[0].Before = 999
	in.OldKeys[0].Plaintext = 999
	in.NewKeys[0].Plaintext = 999
	in.Operations[0].ID = 999
	in.RetiredRotations[0] = 10
	requireCode(t, machine.Verify(p), m.OK)
	p.Receipts[0].After = 0
	p.Fences[0].Retired = false
	requireCode(t, machine.Commit(), m.OK)
	requireCode(t, machine.AdmitEmployeeKey(8, 600, 601), m.OK)
	requireCode(t, machine.AdmitEmployeeKey(8, 600, 999), m.Denied)
}

func TestReorderedInventoryAndProofDoNotChangePolicy(t *testing.T) {
	in, p := fixture()
	for i, j := 0, len(in.Families)-1; i < j; i, j = i+1, j-1 {
		in.Families[i], in.Families[j] = in.Families[j], in.Families[i]
		p.Receipts[i], p.Receipts[j] = p.Receipts[j], p.Receipts[i]
	}
	for i, j := 0, len(p.Fences)-1; i < j; i, j = i+1, j-1 {
		p.Fences[i], p.Fences[j] = p.Fences[j], p.Fences[i]
	}
	machine := prepare(t, in)
	requireCode(t, machine.Verify(p), m.OK)
	requireCode(t, machine.Commit(), m.OK)
}
