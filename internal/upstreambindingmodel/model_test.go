// Independent public literal fixtures only; no actual evidence or fault I/O.
package upstreambindingmodel

import (
	"reflect"
	"testing"
)

type inputs struct {
	o            candidateObservation
	b            expectedBaseline
	c            captureAssumption
	a            authorizationAssumption
	start, final checkpointAssumption
	done         completionAssumption
}

// Expected B/J literals are written independently, never built from O/C/A.
// Repeated literal values are hypothetical assumptions, not verified origins.
func publicInputs() inputs {
	return inputs{
		o: candidateObservation{
			scope:  scope{1, 1, 1, 10, 20, 30, 40, 50, 55, 80, 81, 82, 70, 71, 72, 3},
			schema: schemaBinding{1, 1, 90}, root: rootBinding{60, 61}, generation: 7, watermark: 9,
			shape: recordShape{assumedUnambiguous, []uint8{1, 2, 3, 4, 5, 6, 7, 8}},
			rows:  []rowBinding{{101, openAI}, {102, codex}}, counts: []categoryCount{{openAI, 1}, {anthropic, 0}, {gemini, 0}, {codex, 1}},
			columns: []uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14}, layout: 1, actualSchema: assumedYes, fullDigest: assumedYes,
		},
		b: expectedBaseline{
			origin: assumedExternal,
			scope:  scope{1, 1, 1, 10, 20, 30, 40, 50, 55, 80, 81, 82, 70, 71, 72, 3},
			schema: schemaBinding{1, 1, 90}, shape: recordShape{assumedUnambiguous, []uint8{1, 2, 3, 4, 5, 6, 7, 8}},
			allowed: []generationBinding{{7, rootBinding{60, 61}}},
			rows:    []rowBinding{{101, openAI}, {102, codex}}, counts: []categoryCount{{openAI, 1}, {anthropic, 0}, {gemini, 0}, {codex, 1}},
		},
		c: captureAssumption{
			origin: assumedExternal,
			scope:  scope{1, 1, 1, 10, 20, 30, 40, 50, 55, 80, 81, 82, 70, 71, 72, 3},
			schema: schemaBinding{1, 1, 90}, root: rootBinding{60, 61}, generation: 7, watermark: 9,
			shape:  recordShape{assumedUnambiguous, []uint8{1, 2, 3, 4, 5, 6, 7, 8}},
			method: 1, collectionAuthority: assumedYes, quiesced: assumedYes, sealed: assumedYes, custody: assumedYes,
		},
		a: authorizationAssumption{
			origin: assumedExternal,
			scope:  scope{1, 1, 1, 10, 20, 30, 40, 50, 55, 80, 81, 82, 70, 71, 72, 3},
			schema: schemaBinding{1, 1, 90}, root: rootBinding{60, 61}, generation: 7, watermark: 9,
			shape: recordShape{assumedUnambiguous, []uint8{1, 2, 3, 4, 5, 6, 7, 8}},
		},
		start: checkpointAssumption{
			origin:  assumedExternal,
			scope:   scope{1, 1, 1, 10, 20, 30, 40, 50, 55, 80, 81, 82, 70, 71, 72, 3},
			shape:   recordShape{assumedUnambiguous, []uint8{1, 2, 3, 4, 5, 6, 7, 8}},
			allowed: []generationBinding{{7, rootBinding{60, 61}}}, watermark: 9, replayRevision: 4, current: assumedYes, authorization: unused,
		},
		final: checkpointAssumption{
			origin:  assumedExternal,
			scope:   scope{1, 1, 1, 10, 20, 30, 40, 50, 55, 80, 81, 82, 70, 71, 72, 3},
			shape:   recordShape{assumedUnambiguous, []uint8{1, 2, 3, 4, 5, 6, 7, 8}},
			allowed: []generationBinding{{7, rootBinding{60, 61}}}, watermark: 9, replayRevision: 4, current: assumedYes, authorization: unused,
		},
		done: completionAssumption{enumerated: assumedYes, rowsClosed: assumedYes, rolledBack: assumedYes, connectionReturned: assumedYes, databaseClosed: assumedYes, postChecked: assumedYes, objectStable: assumedYes},
	}
}

func evaluate(f inputs) (*syntheticOnlyDecision, code) {
	return evaluateSynthetic(f.o, f.b, f.c, f.a, f.start, f.final, f.done)
}

func requireCode(t *testing.T, f inputs, want code) {
	t.Helper()
	out, got := evaluate(f)
	if got != want || (want == syntheticOnly && (out == nil || !out.syntheticOnly)) || (want != syntheticOnly && out != nil) {
		t.Fatal("EXACT_CODE_OR_NIL_BOUNDARY")
	}
}

func TestP01IndependentLiteralControl(t *testing.T) {
	f := publicInputs()
	out, got := evaluate(f)
	want := &syntheticOnlyDecision{
		syntheticOnly: true,
		scope:         scope{1, 1, 1, 10, 20, 30, 40, 50, 55, 80, 81, 82, 70, 71, 72, 3},
		schema:        schemaBinding{1, 1, 90}, root: rootBinding{60, 61}, generation: 7, watermark: 9,
		rows: []rowBinding{{101, openAI}, {102, codex}}, counts: []categoryCount{{openAI, 1}, {anthropic, 0}, {gemini, 0}, {codex, 1}},
	}
	if got != syntheticOnly || !reflect.DeepEqual(out, want) || !reflect.DeepEqual(f, publicInputs()) {
		t.Fatal("P01_LITERAL_OR_INPUT_IMMUTABILITY")
	}
}

func publicEmptyInputs() inputs {
	f := publicInputs()
	// Explicit independent new capture literals; no assignment from candidate.
	f.o.scope.snapshot, f.o.scope.digest, f.o.scope.inventory = 41, 83, 84
	f.b.scope.snapshot, f.b.scope.digest, f.b.scope.inventory = 41, 83, 84
	f.c.scope.snapshot, f.c.scope.digest, f.c.scope.inventory = 41, 83, 84
	f.a.scope.snapshot, f.a.scope.digest, f.a.scope.inventory = 41, 83, 84
	f.start.scope.snapshot, f.start.scope.digest, f.start.scope.inventory = 41, 83, 84
	f.final.scope.snapshot, f.final.scope.digest, f.final.scope.inventory = 41, 83, 84
	f.o.rows = nil
	f.b.rows = nil
	f.o.counts = []categoryCount{{openAI, 0}, {anthropic, 0}, {gemini, 0}, {codex, 0}}
	f.b.counts = []categoryCount{{openAI, 0}, {anthropic, 0}, {gemini, 0}, {codex, 0}}
	return f
}

func TestP02EmptyStillRequiresEveryGate(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*inputs)
		want   code
	}{
		{"complete", func(*inputs) {}, syntheticOnly},
		{"missing_column", func(f *inputs) { f.o.columns = f.o.columns[:13] }, badSchema},
		{"missing_zero_category", func(f *inputs) { f.o.counts = f.o.counts[:3] }, badBinding},
		{"missing_authority_field", func(f *inputs) { f.a.shape.fields = nil }, badBinding},
		{"unknown_baseline", func(f *inputs) { f.b.origin = originUnknown }, unknownTrust},
		{"late_cleanup", func(f *inputs) { f.done.rowsClosed = assumedNo }, ioFailure},
	} {
		t.Run(test.name, func(t *testing.T) { f := publicEmptyInputs(); test.change(&f); requireCode(t, f, test.want) })
	}
}

func TestP03FreshRebuiltAndOrderControls(t *testing.T) {
	for _, layout := range []uint8{1, 2} {
		f := publicInputs()
		f.o.layout = layout
		f.o.columns = []uint8{14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
		f.o.rows = []rowBinding{{102, codex}, {101, openAI}}
		f.o.counts = []categoryCount{{codex, 1}, {gemini, 0}, {anthropic, 0}, {openAI, 1}}
		requireCode(t, f, syntheticOnly)
	}
}

func TestP04UnknownRealMechanismsRemainBlocked(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*inputs)
	}{
		{"external_inventory_baseline", func(f *inputs) { f.b.origin = originUnknown }},
		{"collector_verification", func(f *inputs) { f.c.origin = originUnknown }},
		{"issuer_verification", func(f *inputs) { f.a.origin = originUnknown }},
		{"current_external_floor", func(f *inputs) { f.start.origin = originUnknown }},
		{"final_atomic_authority", func(f *inputs) { f.final.origin = originUnknown }},
	} {
		t.Run(test.name, func(t *testing.T) { f := publicInputs(); test.change(&f); requireCode(t, f, unknownTrust) })
	}
}

func TestR01ThroughR30ExactRefusals(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*inputs)
		want   code
	}{
		{"R01_wrong_instance", func(f *inputs) { f.o.scope.instance = 31; f.c.scope.instance = 31 }, badBinding},
		{"R02_wrong_snapshot", func(f *inputs) {
			f.o.scope.snapshot = 41
			f.c.scope.snapshot = 41
			f.o.scope.captureCut = 51
			f.c.scope.captureCut = 51
		}, badBinding},
		{"R03_digest", func(f *inputs) { f.o.scope.digest = 83 }, badBinding},
		{"R04_prefix_coverage", func(f *inputs) { f.o.fullDigest = assumedNo }, badBinding},
		{"R04_length", func(f *inputs) { f.o.scope.length = 84 }, badBinding},
		{"R05_actual_schema", func(f *inputs) { f.o.actualSchema = assumedNo }, badSchema},
		{"R05_unknown_layout", func(f *inputs) { f.o.layout = 3 }, badSchema},
		{"R06_old_generation", func(f *inputs) { f.o.generation = 6; f.c.generation = 6; f.a.generation = 6 }, stale},
		{"R07_future_generation", func(f *inputs) { f.o.generation = 8; f.c.generation = 8; f.a.generation = 8 }, stale},
		{"R08_old_watermark", func(f *inputs) { f.o.watermark = 8; f.c.watermark = 8; f.a.watermark = 8 }, stale},
		{"R09_future_watermark", func(f *inputs) { f.o.watermark = 10 }, stale},
		{"R10_used_authorization", func(f *inputs) { f.start.authorization = used }, replay},
		{"R11_valid_old_canary_wrong_db", func(f *inputs) { f.o.oldCanary = 1; f.o.scope.snapshot = 41 }, badBinding},
		{"R12_authenticated_wrong_capture", func(f *inputs) {
			f.o.scope.snapshot = 41
			f.c.scope.snapshot = 41
			f.o.scope.digest = 83
			f.c.scope.digest = 83
			f.o.scope.inventory = 84
			f.c.scope.inventory = 84
			f.o.rows = []rowBinding{{103, openAI}, {104, codex}}
		}, badBinding},
		{"R12_forged_self_report", func(f *inputs) { f.c.origin = candidateDerived; f.o.scope.digest = 83; f.c.scope.digest = 83 }, unknownTrust},
		{"R13_coherent_omission", func(f *inputs) { f.o.rows = f.o.rows[:1]; f.o.counts[3].count = 0 }, badBinding},
		{"R14_wrong_provider", func(f *inputs) { f.o.rows[1].provider = anthropic; f.o.counts[1].count = 1; f.o.counts[3].count = 0 }, badBinding},
		{"R15_wrong_root", func(f *inputs) { f.o.root.reference = 62; f.c.root.reference = 62; f.a.root.reference = 62 }, badBinding},
		{"R15_wrong_key_management_version", func(f *inputs) { f.c.root.version = 62 }, badBinding},
		{"R16_M1_self_capture", func(f *inputs) { f.c.origin = m1Derived; f.o.oldCanary = 1 }, unknownTrust},
		{"R16_M1_self_authorization", func(f *inputs) { f.a.origin = m1Derived }, unknownTrust},
		{"R17_incomplete_WAL", func(f *inputs) { f.c.method = 2 }, badCapture},
		{"R17_completion_unknown", func(f *inputs) { f.c.sealed = unresolved }, badCapture},
		{"R18_copy_main_only", func(f *inputs) { f.c.method = 3 }, badCapture},
		{"R19_collector_not_approved", func(f *inputs) { f.c.collectionAuthority = assumedNo }, unknownTrust},
		{"R19_collector_identity", func(f *inputs) { f.c.scope.collector = 56 }, unknownTrust},
		{"R19_capture_authorization", func(f *inputs) { f.c.scope.authorization = 73 }, unknownTrust},
		{"R20_expired", func(f *inputs) { f.start.authorization = expired }, replay},
		{"R20_revoked", func(f *inputs) { f.start.authorization = revoked }, replay},
		{"R20_superseded", func(f *inputs) { f.start.authorization = superseded }, replay},
		{"R20_late_expiry", func(f *inputs) { f.final.authorization = expired }, replay},
		{"R21_purpose", func(f *inputs) { f.a.scope.purpose = 2 }, badBinding},
		{"R21_audience", func(f *inputs) { f.a.scope.audience = 11 }, badBinding},
		{"R22_old_baseline", func(f *inputs) { f.start.scope.baselineRevision = 2 }, stale},
		{"R22_stale_checkpoint", func(f *inputs) { f.start.current = assumedNo }, stale},
		{"R23_candidate_baseline", func(f *inputs) { f.b.origin = candidateDerived }, unknownTrust},
		{"R23_missing_baseline", func(f *inputs) { f.b.origin = originUnknown }, unknownTrust},
		{"R23_snapshot_local_checkpoint", func(f *inputs) { f.start.origin = snapshotLocal }, unknownTrust},
		{"R24_duplicate_group", func(f *inputs) { f.o.shape.fields[7] = 7 }, badBinding},
		{"R24_unknown_group", func(f *inputs) { f.o.shape.fields[7] = 9 }, badBinding},
		{"R24_missing_group", func(f *inputs) { f.a.shape.fields = f.a.shape.fields[:7] }, badBinding},
		{"R24_extra_group", func(f *inputs) { f.o.shape.fields = append(f.o.shape.fields, 9) }, badBinding},
		{"R24_ambiguous_encoding_assumption", func(f *inputs) { f.c.shape.encoding = ambiguousEncoding }, badBinding},
		{"R24_omitted_binding", func(f *inputs) { f.a.scope.nonce = 0 }, badBinding},
		{"R25_object_swap", func(f *inputs) { f.done.objectStable = assumedNo }, badCapture},
		{"R26_final_floor", func(f *inputs) { f.final.watermark = 10 }, stale},
		{"R26_final_baseline", func(f *inputs) { f.final.scope.baselineRevision = 4 }, stale},
		{"R26_final_replay_revision", func(f *inputs) { f.final.replayRevision = 5 }, stale},
		{"R27_enumeration_assumption", func(f *inputs) { f.done.enumerated = assumedNo }, ioFailure},
		{"R27_rows_close_assumption", func(f *inputs) { f.done.rowsClosed = assumedNo }, ioFailure},
		{"R27_rollback_assumption", func(f *inputs) { f.done.rolledBack = assumedNo }, ioFailure},
		{"R27_connection_return_assumption", func(f *inputs) { f.done.connectionReturned = assumedNo }, ioFailure},
		{"R27_db_close_assumption", func(f *inputs) { f.done.databaseClosed = assumedNo }, ioFailure},
		{"R27_post_check_assumption", func(f *inputs) { f.done.postChecked = assumedNo }, ioFailure},
		{"R28_start_cancel_assumption", func(f *inputs) { f.done.cancelAtStart = true }, cancelled},
		{"R28_late_cancel_assumption", func(f *inputs) { f.done.cancelBeforePublication = true }, cancelled},
		{"R29_coherent_self_profile", func(f *inputs) { f.o.schema.descriptor = 91; f.c.schema.descriptor = 91; f.a.schema.descriptor = 91 }, badSchema},
		{"R30_old_ledger_floor", func(f *inputs) { f.o.watermark = 8; f.c.watermark = 8; f.a.watermark = 8; f.start.watermark = 8 }, stale},
		{"R30_old_unused_to_current_used", func(f *inputs) { f.final.authorization = used }, replay},
	} {
		t.Run(test.name, func(t *testing.T) { f := publicInputs(); test.change(&f); requireCode(t, f, test.want) })
	}
}

func TestCoordinatedSubstitutionCannotMoveIndependentAnchor(t *testing.T) {
	f := publicInputs()
	f.o.scope.snapshot, f.o.scope.digest, f.o.scope.inventory = 41, 83, 84
	f.c.scope.snapshot, f.c.scope.digest, f.c.scope.inventory = 41, 83, 84
	f.o.rows = []rowBinding{{103, openAI}, {104, codex}}
	requireCode(t, f, badBinding)
	if !reflect.DeepEqual(f.b, publicInputs().b) || !reflect.DeepEqual(f.start, publicInputs().start) {
		t.Fatal("INDEPENDENT_ANCHOR_WAS_MOVED")
	}
}

func TestClosedFieldsAndBoundaries(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*inputs)
		want   code
	}{
		{"zero_symbol", func(f *inputs) { f.o.scope.snapshot = 0 }, badBinding},
		{"oversized_symbol", func(f *inputs) { f.o.scope.snapshot = maxSymbol + 1 }, badBinding},
		{"unknown_contract", func(f *inputs) { f.o.scope.version = 0 }, badBinding},
		{"unknown_hash_algorithm", func(f *inputs) { f.o.scope.digestAlgorithm = 2 }, badBinding},
		{"unknown_canary_tag", func(f *inputs) { f.o.oldCanary = 2 }, badBinding},
		{"unknown_schema_source", func(f *inputs) { f.b.schema.source = 2 }, badSchema},
		{"unknown_schema_descriptor", func(f *inputs) { f.o.schema.descriptor = 0 }, badSchema},
		{"unknown_column", func(f *inputs) { f.o.columns[13] = 15 }, badSchema},
		{"duplicate_column", func(f *inputs) { f.o.columns[13] = 13 }, badSchema},
		{"missing_column", func(f *inputs) { f.o.columns = nil }, badSchema},
		{"duplicate_row", func(f *inputs) { f.o.rows[1].object = 101 }, badBinding},
		{"unknown_provider", func(f *inputs) { f.o.rows[1].provider = 5 }, badBinding},
		{"zero_object", func(f *inputs) { f.o.rows[1].object = 0 }, badBinding},
		{"duplicate_category", func(f *inputs) { f.o.counts[3].provider = openAI }, badBinding},
		{"unknown_category", func(f *inputs) { f.o.counts[3].provider = 5 }, badBinding},
		{"wrong_count", func(f *inputs) { f.o.counts[3].count = 0 }, badBinding},
		{"excess_count", func(f *inputs) { f.o.counts[3].count = 257 }, badBinding},
		{"missing_expected_category", func(f *inputs) { f.b.counts = f.b.counts[:3] }, badBinding},
		{"duplicate_expected_row", func(f *inputs) { f.b.rows[1].object = 101 }, badBinding},
		{"unknown_generation_mapping", func(f *inputs) { f.b.allowed = nil }, badBinding},
		{"duplicate_generation_mapping", func(f *inputs) { f.b.allowed = append(f.b.allowed, generationBinding{7, rootBinding{60, 61}}) }, badBinding},
		{"zero_root_mapping", func(f *inputs) { f.b.allowed[0].root.reference = 0 }, badBinding},
		{"zero_generation", func(f *inputs) { f.c.generation = 0 }, stale},
		{"generation_over_bound", func(f *inputs) { f.c.generation = maxRevision + 1 }, stale},
		{"baseline_revision_over_bound", func(f *inputs) { f.b.scope.baselineRevision = maxRevision + 1 }, badBinding},
		{"watermark_over_bound", func(f *inputs) { f.start.watermark = maxRevision + 1 }, stale},
		{"replay_revision_zero", func(f *inputs) { f.start.replayRevision = 0 }, stale},
		{"checkpoint_unknown_freshness", func(f *inputs) { f.start.current = unresolved }, unknownTrust},
		{"checkpoint_unknown_authorization", func(f *inputs) { f.start.authorization = authorizationUnknown }, unknownTrust},
		{"checkpoint_invalid_origin", func(f *inputs) { f.final.origin = origin(255) }, unknownTrust},
		{"unknown_custody", func(f *inputs) { f.c.custody = unresolved }, badCapture},
		{"unknown_quiescence", func(f *inputs) { f.c.quiesced = unresolved }, badCapture},
		{"unknown_method", func(f *inputs) { f.c.method = 255 }, badCapture},
		{"unknown_full_digest", func(f *inputs) { f.o.fullDigest = certainty(255) }, badBinding},
		{"unknown_cleanup", func(f *inputs) { f.done.rolledBack = unresolved }, ioFailure},
		{"unknown_object_stability", func(f *inputs) { f.done.objectStable = unresolved }, badCapture},
		{"final_mapping_mismatch", func(f *inputs) { f.final.allowed[0].root.version = 62 }, stale},
		{"checkpoint_extra_allowed_generation", func(f *inputs) { f.start.allowed = append(f.start.allowed, generationBinding{8, rootBinding{60, 61}}) }, stale},
		{"final_duplicate_mapping", func(f *inputs) { f.final.allowed = append(f.final.allowed, generationBinding{7, rootBinding{60, 61}}) }, badBinding},
	} {
		t.Run(test.name, func(t *testing.T) { f := publicInputs(); test.change(&f); requireCode(t, f, test.want) })
	}
	for _, role := range []string{"O", "B", "C", "A", "JStart", "JFinal"} {
		t.Run("closed_fields_"+role, func(t *testing.T) {
			f := publicInputs()
			shapes := map[string]*recordShape{"O": &f.o.shape, "B": &f.b.shape, "C": &f.c.shape, "A": &f.a.shape, "JStart": &f.start.shape, "JFinal": &f.final.shape}
			shapes[role].fields[0] = 0
			requireCode(t, f, badBinding)
		})
	}
}

func TestMaximumFiniteSetsAndOverflowRefusals(t *testing.T) {
	f := publicInputs()
	f.o.rows = nil
	f.b.rows = nil
	for i := 1; i <= 256; i++ {
		f.o.rows = append(f.o.rows, rowBinding{symbol(i), openAI})
	}
	for i := 256; i >= 1; i-- {
		f.b.rows = append(f.b.rows, rowBinding{symbol(i), openAI})
	}
	f.o.counts = []categoryCount{{openAI, 256}, {anthropic, 0}, {gemini, 0}, {codex, 0}}
	f.b.counts = []categoryCount{{openAI, 256}, {anthropic, 0}, {gemini, 0}, {codex, 0}}
	requireCode(t, f, syntheticOnly)
	f.o.rows = append(f.o.rows, rowBinding{257, openAI})
	requireCode(t, f, badBinding)
	f = publicInputs()
	f.b.allowed, f.start.allowed, f.final.allowed = nil, nil, nil
	for i := uint32(1); i <= 16; i++ {
		f.b.allowed = append(f.b.allowed, generationBinding{i, rootBinding{60, 61}})
	}
	for i := uint32(16); i >= 1; i-- {
		f.start.allowed = append(f.start.allowed, generationBinding{i, rootBinding{60, 61}})
	}
	for i := uint32(1); i <= 16; i++ {
		f.final.allowed = append(f.final.allowed, generationBinding{i, rootBinding{60, 61}})
	}
	requireCode(t, f, syntheticOnly)
	f.b.allowed = append(f.b.allowed, generationBinding{17, rootBinding{60, 61}})
	requireCode(t, f, badBinding)
	f = publicInputs()
	for _, s := range []*scope{&f.o.scope, &f.b.scope, &f.c.scope, &f.a.scope, &f.start.scope, &f.final.scope} {
		s.snapshot = maxSymbol
		s.baselineRevision = maxRevision
	}
	f.o.generation, f.c.generation, f.a.generation = maxRevision, maxRevision, maxRevision
	f.b.allowed = []generationBinding{{maxRevision, rootBinding{60, 61}}}
	f.start.allowed = []generationBinding{{maxRevision, rootBinding{60, 61}}}
	f.final.allowed = []generationBinding{{maxRevision, rootBinding{60, 61}}}
	f.o.watermark, f.c.watermark, f.a.watermark = maxRevision, maxRevision, maxRevision
	f.start.watermark, f.final.watermark = maxRevision, maxRevision
	f.start.replayRevision, f.final.replayRevision = maxRevision, maxRevision
	requireCode(t, f, syntheticOnly)
}

func TestLateFailuresNeverPublishAndPrimaryIsNotOverwritten(t *testing.T) {
	f := publicInputs()
	f.o.scope.snapshot = 41
	f.done.rowsClosed = assumedNo
	f.done.cancelBeforePublication = true
	requireCode(t, f, badBinding)
	f = publicInputs()
	f.done.rowsClosed = assumedNo
	f.final.authorization = used
	requireCode(t, f, ioFailure)
	f = publicInputs()
	f.done.cancelAtStart = true
	f.b.origin = originUnknown
	requireCode(t, f, cancelled)
	requireCode(t, inputs{}, unknownTrust)
}

func TestInputAndOutputDoNotAlias(t *testing.T) {
	f := publicInputs()
	a, ac := evaluate(f)
	b, bc := evaluate(f)
	if ac != syntheticOnly || bc != syntheticOnly || a == nil || b == nil {
		t.Fatal("ALIAS_CONTROL_FAILED")
	}
	f.o.rows[0].provider = codex
	f.o.shape.fields[0] = 8
	if !reflect.DeepEqual(f.b, publicInputs().b) {
		t.Fatal("CANDIDATE_ALIASES_BASELINE")
	}
	f.b.rows[0].object = 201
	f.b.counts[0].count = 0
	if a.rows[0].object != 101 || a.counts[0].count != 1 {
		t.Fatal("INPUT_MUTATED_PRIOR_DECISION")
	}
	a.rows[0].object = 202
	a.counts[0].count = 2
	if b.rows[0].object != 101 || b.counts[0].count != 1 || f.b.rows[0].object != 201 || f.b.counts[0].count != 0 {
		t.Fatal("OUTPUT_ALIASES_OTHER_OWNER")
	}
}

// Limitation witnesses, NOT successful real authentication or durable consume.
func TestAllExternalAssumptionsCanLieCoherently(t *testing.T) {
	f := publicInputs()
	for _, s := range []*scope{&f.o.scope, &f.b.scope, &f.c.scope, &f.a.scope, &f.start.scope, &f.final.scope} {
		s.instance, s.snapshot, s.digest, s.inventory = 31, 41, 83, 84
	}
	f.o.rows = []rowBinding{{103, openAI}, {104, codex}}
	f.b.rows = []rowBinding{{103, openAI}, {104, codex}}
	requireCode(t, f, syntheticOnly)
	if reflect.DeepEqual(f.b, publicInputs().b) {
		t.Fatal("LIMITATION_WITNESS_DID_NOT_REPLACE_ASSUMPTIONS")
	}
}

func TestRepeatedUnusedCheckpointIsNotConsumption(t *testing.T) {
	f := publicInputs()
	requireCode(t, f, syntheticOnly)
	requireCode(t, f, syntheticOnly)
	if f.start.authorization != unused || f.final.authorization != unused || !reflect.DeepEqual(f, publicInputs()) {
		t.Fatal("PURE_MODEL_MUTATED_EXTERNAL_STATE")
	}
	f.start.authorization = used
	requireCode(t, f, replay)
}
