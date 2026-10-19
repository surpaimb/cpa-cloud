// Package masterkeyrotationmodel is a sequential, symbolic policy model only.
// It accepts no secret bytes, performs no crypto or I/O, and enables no service.
// Provenance: independently authored; see PROVENANCE.md and the model contract.
package masterkeyrotationmodel

// Symbol denotes equality of hypothetical public fixture values, not key bytes.
type Symbol uint64
type Epoch uint64
type Code string

const (
	OK                Code = "MODEL_OK"
	InvalidPlan       Code = "MODEL_INVALID_PLAN"
	InvalidRoot       Code = "MODEL_INVALID_ROOT"
	InvalidCatalog    Code = "MODEL_INVALID_CATALOG"
	InvalidKeys       Code = "MODEL_INVALID_KEYS"
	InvalidOperations Code = "MODEL_INVALID_OPERATIONS"
	InvalidProof      Code = "MODEL_INVALID_PROOF"
	InvalidTransition Code = "MODEL_INVALID_TRANSITION"
	Denied            Code = "MODEL_DENIED"
	Purpose                = "cpacloud/application-root-rotation-model/v1"
	maxRecords             = 4096
)

type Origin uint8

const (
	UnknownOrigin Origin = iota
	Independent
	DerivedFromTarget
	DerivedFromSource
)

type Action uint8

const (
	UnknownAction Action = iota
	FreshDomain
	Reissue
	Retire
	Reencrypt
	RegenerateBinding
	KeepIndependent
)

type Phase uint8

const (
	Unprepared Phase = iota
	Prepared
	Verified
	Committed
	Aborted
)

// Spec is a model audit label, not a production serialization definition.
type Spec struct {
	ID, Purpose, Format        string
	Action                     Action
	Domain                     string
	One, EmptyOnly, Operations bool
}

func spec(id string, action Action, domain string) Spec {
	return Spec{ID: id, Purpose: "rotation/v1/" + id, Format: "source605/" + id, Action: action, Domain: domain}
}

// Catalog returns a new slice. Mutating it cannot change the closed policy.
func Catalog() []Spec {
	items := []Spec{
		spec("domain-digest", FreshDomain, ""),
		spec("domain-aead", FreshDomain, ""),
		spec("domain-response-wrap", FreshDomain, ""),
		spec("domain-response-fingerprint", FreshDomain, ""),
		spec("employee-key", Reissue, "domain-digest"),
		spec("admin-session", Retire, ""),
		spec("self-session", Retire, ""),
		spec("enrollment", Retire, ""),
		spec("oauth-state", Retire, ""),
		spec("audit-cursor", Retire, ""),
		spec("governance-cursor", Retire, ""),
		spec("request-cursor", Retire, ""),
		spec("key-request-cursor", Retire, ""),
		spec("pool-session", Retire, ""),
		spec("proxy-operation", Retire, ""),
		spec("batch-input", Retire, ""),
		spec("batch-operation", Retire, ""),
		spec("redemption-code", Retire, ""),
		spec("upstream-api", Reencrypt, "domain-aead"),
		spec("gemini-api", Reencrypt, "domain-aead"),
		spec("codex-auth", Reencrypt, "domain-aead"),
		spec("oauth-secret", Retire, ""),
		spec("proxy-credential", Reencrypt, "domain-aead"),
		spec("webhook-secret", Reencrypt, "domain-aead"),
		spec("wallet-cursor", Retire, ""),
		spec("classification-cursor", Retire, ""),
		spec("subscription-cursor", Retire, ""),
		spec("catalog-cursor", Retire, ""),
		spec("purchase-quote", Retire, ""),
		spec("renewal-quote", Retire, ""),
		spec("redemption-cursor", Retire, ""),
		spec("adjustment-cursor", Retire, ""),
		spec("topup-cursor", Retire, ""),
		spec("response-dek", Retire, ""),
		spec("response-wrap", Retire, ""),
		spec("response-state", Retire, ""),
		spec("response-operation", Retire, ""),
		spec("response-background", Retire, ""),
		spec("response-managed-tool", Retire, ""),
		spec("installation-canary", RegenerateBinding, ""),
		spec("binding-seed", RegenerateBinding, ""),
		spec("legacy-root-file", Retire, ""),
		spec("portable-binding", Retire, ""),
		spec("backup-v1", Retire, ""),
		spec("backup-v2", Retire, ""),
		spec("backup-v3", Retire, ""),
		spec("backup-generation", Retire, ""),
		spec("independent-w", KeepIndependent, ""),
		spec("bcrypt-password", KeepIndependent, ""),
		spec("unkeyed-ledger", KeepIndependent, ""),
		spec("independent-checksum", KeepIndependent, ""),
		spec("pkce", Retire, ""),
		spec("independent-transient", Retire, ""),
	}
	for i := range items {
		switch items[i].ID {
		case "domain-digest", "domain-aead", "domain-response-wrap", "domain-response-fingerprint", "installation-canary", "binding-seed", "independent-w":
			items[i].One = true
		case "legacy-root-file":
			items[i].EmptyOnly = true
		case "proxy-operation", "batch-input", "batch-operation", "response-operation", "response-background", "response-managed-tool", "backup-generation":
			items[i].Operations = true
		}
	}
	return items
}

type Root struct {
	Value  Symbol
	Epoch  Epoch
	Origin Origin
}

// Family describes an assumed complete source set, including known-empty sets.
// Before is an aggregate identity, not a hash or row-enumeration proof.
type Family struct {
	ID, Format string
	Epoch      Epoch
	Count      uint32
	Before     Symbol
	Complete   bool
}

type OldKey struct{ ID, Plaintext, Digest Symbol }
type NewKey struct {
	OldID, ID, Plaintext, Digest Symbol
	Root, DigestKey              Symbol
	Epoch                        Epoch
	Origin, DigestOrigin         Origin
}

type Outcome uint8

const (
	UnknownOutcome Outcome = iota
	Completed
	Uncertain
)

type Operation struct {
	ID       Symbol
	Consumer string
	Epoch    Epoch
	Outcome  Outcome
}

// Input flags are assumptions to test policy, never attestations of real state.
type Input struct {
	Purpose                                          string
	Rotation, Instance                               Symbol
	Source, Target                                   Root
	MinimumEpoch                                     Epoch
	RetiredRotations                                 []Symbol
	Families                                         []Family
	OldKeys                                          []OldKey
	NewKeys                                          []NewKey
	Operations                                       []Operation
	SnapshotComplete, Quiescent, ObligationsResolved bool
}

// Receipt records a symbolic disposition. Key denotes the M2 purpose-key
// symbol (or M2 itself for direct-root canary/seed). Retired sets have no output.
type Receipt struct {
	ID, Purpose, Format               string
	Action                            Action
	Count, Unresolved                 uint32
	Before, After, Root, Key, Binding Symbol
	Epoch                             Epoch
	Origin                            Origin
	Authenticated, Complete           bool
}

type OperationFence struct {
	ID                      Symbol
	Outcome                 Outcome
	Retired, FactsPreserved bool
}

type Proof struct {
	Purpose                                             string
	Rotation, Instance, SourceRoot, TargetRoot, Binding Symbol
	SourceEpoch, TargetEpoch                            Epoch
	Receipts                                            []Receipt
	Fences                                              []OperationFence
}

// Machine must be obtained from Prepare; its zero value denies all transitions.
// It is intentionally sequential, not goroutine-safe or a durable coordinator.
type Machine struct {
	phase     Phase
	input     Input
	newKeys   map[Symbol]NewKey
	oldValues map[Symbol]bool
}

func addValue(values map[Symbol]bool, value Symbol) bool {
	if value == 0 || values[value] {
		return false
	}
	values[value] = true
	return true
}

func Prepare(in Input) (*Machine, Code) {
	if in.Purpose != Purpose || in.Rotation == 0 || in.Instance == 0 ||
		!in.SnapshotComplete || !in.Quiescent || !in.ObligationsResolved ||
		in.MinimumEpoch == 0 || in.Source.Epoch < in.MinimumEpoch ||
		len(in.RetiredRotations) > maxRecords {
		return nil, InvalidPlan
	}
	history := map[Symbol]bool{}
	for _, id := range in.RetiredRotations {
		if !addValue(history, id) || id == in.Rotation {
			return nil, InvalidPlan
		}
	}
	if in.Source.Value == 0 || in.Source.Epoch == 0 || in.Source.Origin != Independent ||
		in.Target.Value == 0 || in.Target.Value == in.Source.Value ||
		in.Target.Origin != Independent || in.Target.Epoch <= in.Source.Epoch ||
		in.Target.Epoch-in.Source.Epoch != 1 {
		return nil, InvalidRoot
	}
	catalog := Catalog()
	if len(in.Families) != len(catalog) {
		return nil, InvalidCatalog
	}
	families := map[string]Family{}
	values := map[Symbol]bool{in.Source.Value: true}
	for _, f := range in.Families {
		if _, duplicate := families[f.ID]; duplicate {
			return nil, InvalidCatalog
		}
		families[f.ID] = f
	}
	for _, s := range catalog {
		f, exists := families[s.ID]
		if !exists || !f.Complete || f.Format != s.Format || f.Epoch != in.Source.Epoch ||
			f.Count > maxRecords || (s.One && f.Count != 1) || (s.EmptyOnly && f.Count != 0) ||
			(f.Count == 0 && f.Before != 0) || (f.Count > 0 && !addValue(values, f.Before)) {
			return nil, InvalidCatalog
		}
	}
	if len(in.OldKeys) > maxRecords || len(in.NewKeys) != len(in.OldKeys) ||
		len(in.OldKeys) != int(families["employee-key"].Count) {
		return nil, InvalidKeys
	}
	oldKeys := map[Symbol]OldKey{}
	for _, k := range in.OldKeys {
		if !addValue(values, k.ID) || !addValue(values, k.Plaintext) || !addValue(values, k.Digest) {
			return nil, InvalidKeys
		}
		oldKeys[k.ID] = k
	}
	if len(in.Operations) > maxRecords {
		return nil, InvalidOperations
	}
	ops := map[Symbol]bool{}
	opCounts := map[string]uint32{}
	for _, op := range in.Operations {
		validConsumer := false
		for _, s := range catalog {
			if s.ID == op.Consumer && s.Operations {
				validConsumer = true
			}
		}
		if !validConsumer || op.Epoch != in.Source.Epoch ||
			(op.Outcome != Completed && op.Outcome != Uncertain) || !addValue(ops, op.ID) ||
			!addValue(values, op.ID) {
			return nil, InvalidOperations
		}
		opCounts[op.Consumer]++
	}
	for _, s := range catalog {
		if s.Operations && opCounts[s.ID] != families[s.ID].Count {
			return nil, InvalidOperations
		}
	}
	// Target material must not equal any declared source value, not just M1.
	if values[in.Target.Value] {
		return nil, InvalidRoot
	}
	fresh := map[Symbol]bool{in.Target.Value: true}
	reissued := map[Symbol]bool{}
	newKeys := map[Symbol]NewKey{}
	for _, k := range in.NewKeys {
		if _, exists := oldKeys[k.OldID]; !exists || reissued[k.OldID] ||
			k.Origin != Independent || k.DigestOrigin != DerivedFromTarget || k.DigestKey == 0 ||
			k.Root != in.Target.Value || k.Epoch != in.Target.Epoch {
			return nil, InvalidKeys
		}
		for _, v := range []Symbol{k.ID, k.Plaintext, k.Digest} {
			if values[v] || !addValue(fresh, v) {
				return nil, InvalidKeys
			}
		}
		reissued[k.OldID] = true
		newKeys[k.ID] = k
	}
	// All slices are copied; only immutable scalars remain shared with the caller.
	in.Families = append([]Family(nil), in.Families...)
	in.OldKeys = append([]OldKey(nil), in.OldKeys...)
	in.NewKeys = append([]NewKey(nil), in.NewKeys...)
	in.Operations = append([]Operation(nil), in.Operations...)
	in.RetiredRotations = append([]Symbol(nil), in.RetiredRotations...)
	return &Machine{phase: Prepared, input: in, newKeys: newKeys, oldValues: values}, OK
}

func (m *Machine) Verify(p Proof) Code {
	if m == nil || m.phase != Prepared {
		return InvalidTransition
	}
	in := m.input
	if p.Purpose != Purpose || p.Rotation != in.Rotation || p.Instance != in.Instance ||
		p.SourceRoot != in.Source.Value || p.TargetRoot != in.Target.Value ||
		p.SourceEpoch != in.Source.Epoch || p.TargetEpoch != in.Target.Epoch ||
		p.Binding == 0 || p.Binding == in.Target.Value || m.oldValues[p.Binding] {
		return InvalidProof
	}
	catalog := Catalog()
	if len(p.Receipts) != len(catalog) || len(p.Fences) != len(in.Operations) {
		return InvalidProof
	}
	receipts := map[string]Receipt{}
	for _, r := range p.Receipts {
		if _, duplicate := receipts[r.ID]; duplicate {
			return InvalidProof
		}
		receipts[r.ID] = r
	}
	for _, k := range in.NewKeys {
		if k.DigestKey != receipts["domain-digest"].After {
			return InvalidProof
		}
	}
	outputs := map[Symbol]bool{in.Target.Value: true, p.Binding: true}
	for _, k := range in.NewKeys {
		for _, v := range []Symbol{k.ID, k.Plaintext, k.Digest} {
			if !addValue(outputs, v) {
				return InvalidProof
			}
		}
	}
	families := map[string]Family{}
	for _, f := range in.Families {
		families[f.ID] = f
	}
	for _, s := range catalog {
		r, exists := receipts[s.ID]
		f := families[s.ID]
		if !exists || r.Purpose != s.Purpose || r.Format != s.Format || r.Action != s.Action ||
			r.Count != f.Count || r.Before != f.Before || r.Unresolved != 0 || !r.Complete ||
			r.Epoch != in.Target.Epoch {
			return InvalidProof
		}
		if r.Count == 0 {
			if r.After != 0 || r.Root != 0 || r.Key != 0 || r.Binding != 0 ||
				r.Origin != UnknownOrigin || r.Authenticated {
				return InvalidProof
			}
			continue
		}
		switch s.Action {
		case Retire:
			if r.After != 0 || r.Root != 0 || r.Key != 0 || r.Binding != 0 ||
				r.Origin != UnknownOrigin || r.Authenticated {
				return InvalidProof
			}
		case KeepIndependent:
			if r.After != r.Before || r.Root != 0 || r.Key != 0 || r.Binding != 0 ||
				r.Origin != Independent || r.Authenticated {
				return InvalidProof
			}
		default:
			if r.Root != in.Target.Value || r.Binding != p.Binding || !r.Authenticated ||
				m.oldValues[r.After] || !addValue(outputs, r.After) {
				return InvalidProof
			}
			switch s.Action {
			case FreshDomain:
				if r.Origin != DerivedFromTarget || r.Key != in.Target.Value {
					return InvalidProof
				}
			case Reissue, Reencrypt:
				key, found := receipts[s.Domain]
				if !found || r.Origin != DerivedFromTarget || r.Key != key.After {
					return InvalidProof
				}
			case RegenerateBinding:
				if r.Origin != DerivedFromTarget || r.Key != in.Target.Value {
					return InvalidProof
				}
			default:
				return InvalidProof
			}
		}
	}
	fences := map[Symbol]OperationFence{}
	for _, f := range p.Fences {
		if _, duplicate := fences[f.ID]; duplicate {
			return InvalidProof
		}
		fences[f.ID] = f
	}
	for _, op := range in.Operations {
		f, exists := fences[op.ID]
		if !exists || f.Outcome != op.Outcome || !f.Retired || !f.FactsPreserved {
			return InvalidProof
		}
	}
	// No caller-owned proof references are retained. A refusal never advances state.
	m.phase = Verified
	return OK
}

func (m *Machine) Commit() Code {
	if m == nil || m.phase != Verified {
		return InvalidTransition
	}
	m.phase = Committed
	return OK
}

func (m *Machine) Abort() Code {
	if m == nil || (m.phase != Prepared && m.phase != Verified) {
		return InvalidTransition
	}
	m.phase = Aborted
	return OK
}

func (m *Machine) Phase() Phase {
	if m == nil {
		return Unprepared
	}
	return m.phase
}

// CanServe is only a symbolic predicate; it starts no service or callback.
func (m *Machine) CanServe() bool { return m != nil && m.phase == Committed }

func (m *Machine) AdmitEmployeeKey(epoch Epoch, id, plaintext Symbol) Code {
	if !m.CanServe() || epoch != m.input.Target.Epoch {
		return Denied
	}
	k, exists := m.newKeys[id]
	if !exists || k.Plaintext != plaintext {
		return Denied
	}
	return OK
}

// RetiredState, operations and backup packages are deliberately never admitted.
// This model has no new-work dispatch or new-generation restore implementation.
func (m *Machine) AdmitRetiredState(epoch Epoch, value Symbol) Code { return Denied }
func (m *Machine) AdmitOperation(id Symbol) Code                    { return Denied }
func (m *Machine) AdmitRestore(epoch Epoch, root Symbol) Code       { return Denied }
