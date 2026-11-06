// Independently authored synthetic policy only. No production API or I/O.
package upstreambindingmodel

type code string

const (
	syntheticOnly code = "SB_SYNTHETIC_ONLY"
	unknownTrust  code = "SB_UNKNOWN_TRUST"
	badBinding    code = "SB_BINDING"
	badSchema     code = "SB_SCHEMA"
	badCapture    code = "SB_CAPTURE"
	stale         code = "SB_STALE"
	replay        code = "SB_REPLAY"
	ioFailure     code = "SB_IO"
	cancelled     code = "SB_CANCELLED"
	maxSymbol          = 4095
	maxRows            = 256
	maxMappings        = 16
	maxRevision        = 1 << 20
)

// Symbols denote public hypothetical values, never bytes, keys or digests.
type symbol uint16
type origin uint8

const (
	originUnknown origin = iota
	assumedExternal
	candidateDerived
	m1Derived
	snapshotLocal
)

type certainty uint8

const (
	unresolved certainty = iota
	assumedYes
	assumedNo
)

type encoding uint8

const (
	encodingUnknown encoding = iota
	assumedUnambiguous
	ambiguousEncoding
)

// Eight authority-field groups, not an implemented serialization grammar.
type recordShape struct {
	encoding encoding
	fields   []uint8
}

type scope struct {
	version, purpose, digestAlgorithm uint8
	audience, baseline, instance      symbol
	snapshot, captureCut, collector   symbol
	digest, length, inventory         symbol
	authorization, nonce, evaluation  symbol
	baselineRevision                  uint32
}

type schemaBinding struct {
	profile, source uint8 // 1 denotes the frozen PR92 profile/source, not SQL.
	descriptor      symbol
}

type rootBinding struct{ reference, version symbol }
type generationBinding struct {
	generation uint32
	root       rootBinding
}
type provider uint8

const (
	openAI provider = iota + 1
	anthropic
	gemini
	codex
)

type rowBinding struct {
	object   symbol
	provider provider
}
type categoryCount struct {
	provider provider
	count    uint16
}

// These roles are intentionally different types. That does not authenticate
// their provenance; same-package callers could still lie about every input.
type candidateObservation struct {
	scope                    scope // Candidate proposals/claims, not authority.
	schema                   schemaBinding
	root                     rootBinding
	generation, watermark    uint32
	shape                    recordShape
	rows                     []rowBinding
	counts                   []categoryCount
	columns                  []uint8
	layout                   uint8 // 1=fresh, 2=rebuilt; no parser is implemented.
	actualSchema, fullDigest certainty
	oldCanary                uint8 // 0=absent, 1=assumed valid old canary; not proof.
}

type expectedBaseline struct {
	origin  origin
	scope   scope
	schema  schemaBinding
	shape   recordShape
	allowed []generationBinding
	rows    []rowBinding
	counts  []categoryCount
}

type captureAssumption struct {
	origin                                origin
	scope                                 scope
	schema                                schemaBinding
	root                                  rootBinding
	generation, watermark                 uint32
	shape                                 recordShape
	method                                uint8 // 1=assumed offline, 2=WAL, 3=main-only.
	collectionAuthority, quiesced, sealed certainty
	custody                               certainty
}

type authorizationAssumption struct {
	origin                origin
	scope                 scope
	schema                schemaBinding
	root                  rootBinding
	generation, watermark uint32
	shape                 recordShape
}

type authorizationState uint8

const (
	authorizationUnknown authorizationState = iota
	unused
	used
	expired
	revoked
	superseded
)

type checkpointAssumption struct {
	origin                    origin
	scope                     scope
	shape                     recordShape
	allowed                   []generationBinding
	watermark, replayRevision uint32
	current                   certainty
	authorization             authorizationState
}

type completionAssumption struct {
	cancelAtStart, cancelBeforePublication bool
	enumerated, rowsClosed, rolledBack     certainty
	connectionReturned, databaseClosed     certainty
	postChecked, objectStable              certainty
}

type syntheticOnlyDecision struct {
	syntheticOnly         bool
	scope                 scope
	schema                schemaBinding
	root                  rootBinding
	generation, watermark uint32
	rows                  []rowBinding
	counts                []categoryCount
}

func validSymbol(s symbol) bool   { return s > 0 && s <= maxSymbol }
func validRevision(v uint32) bool { return v > 0 && v <= maxRevision }

func validScope(s scope) bool {
	if s.version != 1 || s.purpose != 1 || s.digestAlgorithm != 1 || !validRevision(s.baselineRevision) {
		return false
	}
	for _, v := range [...]symbol{s.audience, s.baseline, s.instance, s.snapshot, s.captureCut, s.collector, s.digest, s.length, s.inventory, s.authorization, s.nonce, s.evaluation} {
		if !validSymbol(v) {
			return false
		}
	}
	return true
}

func validShape(s recordShape) bool {
	if s.encoding != assumedUnambiguous || len(s.fields) != 8 {
		return false
	}
	var seen [9]bool
	for _, field := range s.fields {
		if field < 1 || field > 8 || seen[field] {
			return false
		}
		seen[field] = true
	}
	return true
}

func validSchema(s schemaBinding) bool {
	return s.profile == 1 && s.source == 1 && validSymbol(s.descriptor)
}

func validMappings(m []generationBinding) bool {
	if len(m) == 0 || len(m) > maxMappings {
		return false
	}
	for i, v := range m {
		if !validRevision(v.generation) || !validSymbol(v.root.reference) || !validSymbol(v.root.version) {
			return false
		}
		for j := 0; j < i; j++ {
			if m[j].generation == v.generation {
				return false
			}
		}
	}
	return true
}

func mappedRoot(m []generationBinding, generation uint32) (rootBinding, bool) {
	for _, v := range m {
		if v.generation == generation {
			return v.root, true
		}
	}
	return rootBinding{}, false
}

func sameMappings(a, b []generationBinding) bool {
	if len(a) != len(b) {
		return false
	}
	for _, v := range a {
		if root, found := mappedRoot(b, v.generation); !found || root != v.root {
			return false
		}
	}
	return true
}

func validInventory(rows []rowBinding, counts []categoryCount) bool {
	if len(rows) > maxRows || len(counts) != 4 {
		return false
	}
	var actual [5]uint16
	for i, row := range rows {
		if !validSymbol(row.object) || row.provider < openAI || row.provider > codex {
			return false
		}
		for j := 0; j < i; j++ {
			if rows[j].object == row.object {
				return false
			}
		}
		actual[row.provider]++
	}
	var seen [5]bool
	for _, count := range counts {
		if count.provider < openAI || count.provider > codex || seen[count.provider] || count.count > maxRows || count.count != actual[count.provider] {
			return false
		}
		seen[count.provider] = true
	}
	return true
}

func sameRows(a, b []rowBinding) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		found := false
		for _, y := range b {
			if x == y {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func validColumns(columns []uint8) bool {
	if len(columns) != 14 {
		return false
	}
	var seen [15]bool
	for _, c := range columns {
		if c < 1 || c > 14 || seen[c] {
			return false
		}
		seen[c] = true
	}
	return true
}

func checkCheckpoint(j checkpointAssumption, b expectedBaseline) code {
	if j.origin != assumedExternal || j.current == unresolved || j.current > assumedNo || j.authorization == authorizationUnknown || j.authorization > superseded {
		return unknownTrust
	}
	if !validShape(j.shape) || !validScope(j.scope) || !validMappings(j.allowed) {
		return badBinding
	}
	if j.current != assumedYes || j.scope != b.scope || !sameMappings(j.allowed, b.allowed) || !validRevision(j.watermark) || !validRevision(j.replayRevision) {
		return stale
	}
	if j.authorization != unused {
		return replay
	}
	return syntheticOnly
}

// Pure assumption evaluation only. It neither authenticates any input nor
// consumes authorization. Reusing identical unused checkpoints repeats success.
// No real-source adapter, exported constructor, callback or production caller.
func evaluateSynthetic(o candidateObservation, b expectedBaseline, c captureAssumption, a authorizationAssumption, start, final checkpointAssumption, done completionAssumption) (*syntheticOnlyDecision, code) {
	if done.cancelAtStart {
		return nil, cancelled
	}
	if b.origin != assumedExternal || c.origin != assumedExternal || a.origin != assumedExternal || c.collectionAuthority != assumedYes {
		return nil, unknownTrust
	}
	if !validScope(b.scope) || !validScope(o.scope) || !validScope(c.scope) || !validScope(a.scope) || !validMappings(b.allowed) || o.oldCanary > 1 {
		return nil, badBinding
	}
	for _, shape := range [...]recordShape{o.shape, b.shape, c.shape, a.shape} {
		if !validShape(shape) {
			return nil, badBinding
		}
	}
	if result := checkCheckpoint(start, b); result != syntheticOnly {
		return nil, result
	}
	if c.scope.collector != b.scope.collector || c.scope.authorization != a.scope.authorization {
		return nil, unknownTrust
	}
	if o.scope != b.scope || c.scope != b.scope || a.scope != b.scope || o.fullDigest != assumedYes {
		return nil, badBinding
	}
	if !validRevision(c.generation) || o.generation != c.generation || a.generation != c.generation {
		return nil, stale
	}
	root, allowed := mappedRoot(b.allowed, c.generation)
	if !allowed {
		return nil, stale
	}
	if c.root != root || o.root != root || a.root != root {
		return nil, badBinding
	}
	if c.watermark != start.watermark || o.watermark != start.watermark || a.watermark != start.watermark {
		return nil, stale
	}
	if c.method != 1 || c.quiesced != assumedYes || c.sealed != assumedYes || c.custody != assumedYes {
		return nil, badCapture
	}
	if !validSchema(b.schema) || o.schema != b.schema || c.schema != b.schema || a.schema != b.schema || o.actualSchema != assumedYes || (o.layout != 1 && o.layout != 2) || !validColumns(o.columns) {
		return nil, badSchema
	}
	if !validInventory(b.rows, b.counts) || !validInventory(o.rows, o.counts) || !sameRows(o.rows, b.rows) {
		return nil, badBinding
	}
	if done.objectStable != assumedYes {
		return nil, badCapture
	}
	for _, phase := range [...]certainty{done.enumerated, done.rowsClosed, done.rolledBack, done.connectionReturned, done.databaseClosed, done.postChecked} {
		if phase != assumedYes {
			return nil, ioFailure
		}
	}
	if result := checkCheckpoint(final, b); result != syntheticOnly {
		return nil, result
	}
	if final.watermark != start.watermark || final.replayRevision != start.replayRevision {
		return nil, stale
	}
	if done.cancelBeforePublication {
		return nil, cancelled
	}
	return &syntheticOnlyDecision{
		syntheticOnly: true, scope: b.scope, schema: b.schema, root: root,
		generation: c.generation, watermark: start.watermark,
		rows: append([]rowBinding(nil), b.rows...), counts: append([]categoryCount(nil), b.counts...),
	}, syntheticOnly
}
