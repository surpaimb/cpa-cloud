// Independently authored for docs/employee-self-logout-collector-algorithm-contract-2026-10-21.md.
// Public semantic sources: Go1.26.9 src/sync/atomic/{doc,type}.go (BSD-style license).
// Test-only synthetic algorithm A; no App/SQL/driver/opener or real identity bridge.
package service

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

const (
	slogACap   = 8192
	slogACuts  = 128
	slogASeal  = uint64(1) << 63
	slogAMask  = uint64(1<<14) - 1
	slogAEmpty = uint32(0)
	slogAWrite = uint32(1)
	slogAReady = uint32(2)
)

type slogAFault uint8

const (
	slogAGateFault slogAFault = iota
	slogABoundFault
	slogAOverflow
	slogAIdentity
	slogATokenFault
	slogADuplicateEnter
	slogAReturnOrder
	slogADuplicateReturn
	slogAPhaseFault
	slogACutRange
	slogADuplicateCut
	slogAEnumFault
	slogAFaults
	slogANoFault slogAFault = 255
)

type slogAResult uint8

const (
	slogAOK slogAResult = iota
	slogABadConn
	slogACancelled
	slogADeadline
	slogASQLite5
	slogASQLiteOther
	slogAOther
)

type slogAKind uint8

const (
	slogARollback slogAKind = 1
	slogAClose    slogAKind = 2
)

type slogAMeta struct {
	app, conn, tx, pass uint16
	kind                slogAKind
}

func (m slogAMeta) valid() bool {
	if m.app == 0 || m.app > 16 || m.conn == 0 || m.conn > 32 {
		return false
	}
	if m.kind == slogAClose {
		return m.tx == 0 && m.pass == 0
	}
	return m.kind == slogARollback && m.tx > 0 && m.tx <= 128 && m.pass > 0 && m.pass <= 128
}

type slogASlot struct {
	enterPhase  atomic.Uint32
	returnPhase atomic.Uint32
	enter       slogAMeta
	result      slogAResult
}

type slogARowState uint8

const (
	slogANotScanned slogARowState = iota
	slogAReserved
	slogAOpen
	slogAComplete
)

type slogARow struct {
	enter  slogAMeta
	result slogAResult
	state  slogARowState
}

type slogAView struct {
	phase      atomic.Uint32
	h, scanned uint16
	sealed     bool
	late       bool
	faults     uint16
}

type slogASummary struct {
	h, scanned, faults  uint16
	ready, sealed, late bool
	postSealUnknown     bool
}

// Never copied/reset after first use. All backing storage is construction-only.
type slogCollectorA struct {
	gate   atomic.Uint64
	late   atomic.Bool // only an admission attempt that observed SEALED
	faults [slogAFaults]atomic.Bool
	n, k   uint16
	slots  []slogASlot
	rows   []slogARow
	views  []slogAView
}

type slogAToken struct {
	owner    *slogCollectorA
	meta     slogAMeta
	index    uint16
	attempts uint16
	mode     uint8 // 1 admitted, 2 suppressed; zero is invalid
	reason   uint8 // suppressed: 1 sealed, 2 overflow, 3 invalid
}

type slogAViewToken struct {
	owner *slogCollectorA
	index uint16
}

type slogASealed struct {
	h, attempts uint16
	ok          bool
}

func newSlogCollectorA(n, k int) *slogCollectorA {
	if n < 1 || n > slogACap || k < 1 || k > slogACuts {
		return nil
	}
	return &slogCollectorA{
		n: uint16(n), k: uint16(k),
		slots: make([]slogASlot, n), rows: make([]slogARow, n*k), views: make([]slogAView, k),
	}
}

func (c *slogCollectorA) fail(f slogAFault) { c.faults[f].Store(true) }

func (c *slogCollectorA) faultMask() uint16 {
	var mask uint16
	for f := slogAFault(0); f < slogAFaults; f++ {
		if c.faults[f].Load() {
			mask |= uint16(1) << f
		}
	}
	return mask
}

func (c *slogCollectorA) validGate(s uint64) bool {
	return s & ^(slogASeal|slogAMask) == 0 && s&slogAMask <= uint64(c.n)
}

// Same guarded primitive is used by Reserve and deterministic stale-CAS controls.
func (c *slogCollectorA) tryReserve(s uint64) bool {
	return c.validGate(s) && s&slogASeal == 0 && s&slogAMask < uint64(c.n) && c.gate.CompareAndSwap(s, s+1)
}

func (c *slogCollectorA) trySeal(s uint64) bool {
	return c.validGate(s) && s&slogASeal == 0 && c.gate.CompareAndSwap(s, s|slogASeal)
}

func (c *slogCollectorA) suppressed(reason uint8, attempts uint16) slogAToken {
	return slogAToken{owner: c, mode: 2, reason: reason, attempts: attempts}
}

func (c *slogCollectorA) reserve(m slogAMeta) slogAToken {
	if !m.valid() {
		c.fail(slogAIdentity)
		return c.suppressed(3, 0)
	}
	for i := uint16(1); i <= c.n+2; i++ {
		s := c.gate.Load()
		if !c.validGate(s) {
			c.fail(slogAGateFault)
			return c.suppressed(3, i)
		}
		if s&slogASeal != 0 {
			c.late.Store(true)
			return c.suppressed(1, i)
		}
		if s&slogAMask == uint64(c.n) {
			c.fail(slogAOverflow)
			return c.suppressed(2, i)
		}
		if c.tryReserve(s) {
			return slogAToken{owner: c, meta: m, index: uint16(s & slogAMask), attempts: i, mode: 1}
		}
	}
	c.fail(slogABoundFault)
	return c.suppressed(3, c.n+2)
}

func (c *slogCollectorA) seal() slogASealed {
	for i := uint16(1); i <= c.n+2; i++ {
		s := c.gate.Load()
		if !c.validGate(s) {
			c.fail(slogAGateFault)
			return slogASealed{attempts: i}
		}
		if s&slogASeal != 0 || c.trySeal(s) {
			return slogASealed{h: uint16(s & slogAMask), attempts: i, ok: true}
		}
	}
	c.fail(slogABoundFault)
	return slogASealed{attempts: c.n + 2}
}

func (c *slogCollectorA) resolve(t slogAToken) *slogASlot {
	if t.owner != c {
		c.fail(slogATokenFault)
		return nil
	}
	if t.mode == 2 {
		return nil
	}
	if t.mode != 1 || t.index >= c.n {
		c.fail(slogATokenFault)
		return nil
	}
	s := c.gate.Load()
	if !c.validGate(s) || uint64(t.index) >= s&slogAMask {
		c.fail(slogATokenFault)
		return nil
	}
	return &c.slots[t.index]
}

func (c *slogCollectorA) claimEnter(t slogAToken) *slogASlot {
	s := c.resolve(t)
	if s == nil {
		return nil
	}
	if !t.meta.valid() {
		c.fail(slogAIdentity)
		return nil
	}
	if !s.enterPhase.CompareAndSwap(slogAEmpty, slogAWrite) {
		c.fail(slogADuplicateEnter)
		return nil
	}
	return s
}

func slogAWriteEnter(s *slogASlot, m slogAMeta) { s.enter = m }
func slogAReadyEnter(s *slogASlot)              { s.enterPhase.Store(slogAReady) }

func (c *slogCollectorA) enter(t slogAToken) bool {
	s := c.claimEnter(t)
	if s == nil {
		return false
	}
	slogAWriteEnter(s, t.meta)
	slogAReadyEnter(s)
	return true
}

func (c *slogCollectorA) claimReturn(t slogAToken, result slogAResult) *slogASlot {
	s := c.resolve(t)
	if s == nil {
		return nil
	}
	if result > slogAOther {
		c.fail(slogAEnumFault)
		return nil
	}
	if s.enterPhase.Load() != slogAReady {
		c.fail(slogAReturnOrder)
		return nil
	}
	if t.meta != s.enter {
		c.fail(slogATokenFault)
		return nil
	}
	if !s.returnPhase.CompareAndSwap(slogAEmpty, slogAWrite) {
		c.fail(slogADuplicateReturn)
		return nil
	}
	return s
}

func slogAWriteReturn(s *slogASlot, r slogAResult) { s.result = r }
func slogAReadyReturn(s *slogASlot)                { s.returnPhase.Store(slogAReady) }

func (c *slogCollectorA) returned(t slogAToken, result slogAResult) bool {
	s := c.claimReturn(t, result)
	if s == nil {
		return false
	}
	slogAWriteReturn(s, result)
	slogAReadyReturn(s)
	return true
}

// The order is part of the algorithm, not interchangeable independent loads.
func slogAReadSlot(s *slogASlot) (slogARow, slogAFault) {
	r := s.returnPhase.Load()
	e := s.enterPhase.Load()
	return slogADecodeSlot(s, r, e)
}

func slogADecodeSlot(s *slogASlot, r, e uint32) (slogARow, slogAFault) {
	if r > slogAReady || e > slogAReady || (r != slogAEmpty && e != slogAReady) {
		return slogARow{}, slogAPhaseFault
	}
	row := slogARow{state: slogAReserved}
	if e == slogAReady {
		row.enter = s.enter
		row.state = slogAOpen
	}
	if r == slogAReady {
		row.result = s.result
		row.state = slogAComplete
	}
	return row, slogANoFault
}

func (c *slogCollectorA) finishView(v *slogAView) {
	v.faults = c.faultMask()
	v.late = c.late.Load()
	v.phase.Store(slogAReady)
}

func (c *slogCollectorA) cut(id int) slogAViewToken {
	if id < 0 || id >= int(c.k) {
		c.fail(slogACutRange)
		return slogAViewToken{}
	}
	v := &c.views[id]
	if !v.phase.CompareAndSwap(slogAEmpty, slogAWrite) {
		c.fail(slogADuplicateCut)
		return slogAViewToken{}
	}
	out := slogAViewToken{owner: c, index: uint16(id)}
	s := c.gate.Load()
	if !c.validGate(s) {
		c.fail(slogAGateFault)
		c.finishView(v)
		return out
	}
	v.h, v.sealed = uint16(s&slogAMask), s&slogASeal != 0
	for i := uint16(0); i < v.h; i++ {
		row, fault := slogAReadSlot(&c.slots[i])
		if fault != slogANoFault {
			c.fail(fault)
			break
		}
		c.rows[id*int(c.n)+int(i)] = row
		v.scanned++
	}
	c.finishView(v)
	return out
}

func (c *slogCollectorA) sealCut(id int) slogAViewToken {
	if !c.seal().ok {
		return slogAViewToken{}
	}
	return c.cut(id)
}

func (c *slogCollectorA) summary(t slogAViewToken) slogASummary {
	if t.owner != c || t.index >= c.k {
		c.fail(slogATokenFault)
		return slogASummary{}
	}
	v := &c.views[t.index]
	if v.phase.Load() != slogAReady {
		return slogASummary{}
	}
	return slogASummary{h: v.h, scanned: v.scanned, faults: v.faults, ready: true, sealed: v.sealed, late: v.late, postSealUnknown: v.sealed}
}

func (c *slogCollectorA) row(t slogAViewToken, index int) (slogARow, bool) {
	s := c.summary(t)
	if !s.ready || index < 0 || index >= int(s.h) {
		return slogARow{}, false
	}
	return c.rows[int(t.index)*int(c.n)+index], true
}

// Synthetic controls only below. Barriers, joins, testing.T and timers are NOT
// called from collector methods. No actual App/driver invocation is represented.
func slogACheck(t *testing.T, ok bool, label string) {
	t.Helper()
	if !ok {
		t.Fatal(label)
	}
}

func slogANewTest(t *testing.T, n, k int) *slogCollectorA {
	t.Helper()
	slogACheck(t, runtime.Version() == "go1.26.9", "SCA_TOOLCHAIN")
	c := newSlogCollectorA(n, k)
	slogACheck(t, c != nil, "SCA_CONSTRUCTOR")
	return c
}

func slogAM() slogAMeta { return slogAMeta{app: 1, conn: 1, tx: 1, pass: 1, kind: slogARollback} }

func slogAWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Fatal("SCA_HARNESS_DEADLINE")
	}
}

func slogAJoined(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	slogAWait(t, done)
}

func slogAStart(start <-chan struct{}) bool {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-start:
		return true
	case <-timer.C:
		return false
	}
}

func TestSelfLogoutCollector20261021Admission(t *testing.T) {
	c := slogANewTest(t, 4, 4)
	a, b := c.reserve(slogAM()), c.reserve(slogAMeta{app: 16, conn: 32, kind: slogAClose})
	slogACheck(t, a.mode == 1 && b.mode == 1 && a.index == 0 && b.index == 1, "SCA_ORDINAL")
	slogACheck(t, c.enter(a) && c.returned(a, slogASQLite5) && c.enter(b) && c.returned(b, slogAOK), "SCA_PUBLISH")
	v := c.cut(0)
	s := c.summary(v)
	r, ok := c.row(v, 1)
	slogACheck(t, s.ready && s.h == 2 && s.scanned == 2 && s.faults == 0 && !s.sealed, "SCA_SUMMARY")
	slogACheck(t, ok && r.state == slogAComplete && r.enter.tx == 0 && r.enter.pass == 0, "SCA_CLOSE_NOT_PASS")
}

func TestSelfLogoutCollector20261021SealInterleavings(t *testing.T) {
	c := slogANewTest(t, 4, 4)
	tok := c.reserve(slogAM())
	stale := c.gate.Load()
	sealed := c.seal()
	slogACheck(t, sealed.ok && sealed.h == 1 && !c.tryReserve(stale), "SCA_STALE_CAS")
	v := c.cut(0)
	r, _ := c.row(v, 0)
	slogACheck(t, r.state == slogAReserved, "SCA_RESERVED")
	slogACheck(t, c.enter(tok) && c.returned(tok, slogAOther), "SCA_OLD_ADMISSION_LATE_PUBLISH")
	late := c.reserve(slogAM())
	slogACheck(t, late.mode == 2 && late.reason == 1 && !c.enter(late) && !c.returned(late, slogAOK), "SCA_LATE_ADMISSION")
	again := c.seal()
	old, _ := c.row(v, 0)
	slogACheck(t, again.ok && again.h == 1 && old == r && c.faultMask() == 0, "SCA_SEAL_IDEMPOTENT_OLD_VIEW")
	d := slogANewTest(t, 1, 1)
	slogACheck(t, d.seal().h == 0 && d.reserve(slogAM()).reason == 1 && d.gate.Load() == slogASeal, "SCA_SEAL_FIRST")
}

func TestSelfLogoutCollector20261021PublicationCuts(t *testing.T) {
	c := slogANewTest(t, 1, 4)
	tok := c.reserve(slogAM())
	s := c.claimEnter(tok)
	slogACheck(t, s != nil, "SCA_ENTER_CLAIM")
	slogAWriteEnter(s, tok.meta)
	v0 := c.cut(0)
	r0, _ := c.row(v0, 0)
	slogACheck(t, r0.state == slogAReserved && r0.enter == (slogAMeta{}), "SCA_UNREADY_ENTER_HIDDEN")
	slogAReadyEnter(s)
	rs := c.claimReturn(tok, slogASQLite5)
	slogACheck(t, rs != nil, "SCA_RETURN_CLAIM")
	slogAWriteReturn(rs, slogASQLite5)
	v1 := c.cut(1)
	r1, _ := c.row(v1, 0)
	slogACheck(t, r1.state == slogAOpen && r1.result == slogAOK, "SCA_UNREADY_RETURN_HIDDEN")
	c.seal()
	slogAReadyReturn(rs)
	v2 := c.cut(2)
	r2, _ := c.row(v2, 0)
	a, _ := c.row(v0, 0)
	b, _ := c.row(v1, 0)
	slogACheck(t, r2.state == slogAComplete && r2.result == slogASQLite5 && c.summary(v2).sealed && c.summary(v2).postSealUnknown, "SCA_SEALED_SCAN_NOT_SEAL_INSTANT")
	slogACheck(t, a == r0 && b == r1 && c.faultMask() == 0 && !c.summary(v2).late, "SCA_OLD_CUT_IMMUTABLE_TAIL_NOT_FLAG")
}

func TestSelfLogoutCollector20261021ReadOrderWitness(t *testing.T) {
	c := slogANewTest(t, 1, 1)
	tok := c.reserve(slogAM())
	s := &c.slots[0]
	rEarly, eEarly := s.returnPhase.Load(), s.enterPhase.Load()
	slogACheck(t, c.enter(tok) && c.returned(tok, slogAOther), "SCA_ORDER_SETUP")
	eLate, rLate := s.enterPhase.Load(), s.returnPhase.Load()
	good, fault := slogADecodeSlot(s, rEarly, eLate)
	slogACheck(t, fault == slogANoFault && good.state == slogAOpen && good.result == slogAOK, "SCA_VALID_MIXED_OPEN")
	_, badOrderFault := slogADecodeSlot(s, rLate, eEarly)
	slogACheck(t, eEarly == slogAEmpty && rLate == slogAReady && badOrderFault == slogAPhaseFault, "SCA_REVERSED_ORDER_FALSE_ANOMALY_WITNESS")
	r, _ := c.row(c.cut(0), 0)
	slogACheck(t, r.state == slogAComplete && c.faultMask() == 0, "SCA_ACTUAL_SCAN_ORDER")
}

func TestSelfLogoutCollector20261021ConcurrentUnique(t *testing.T) {
	c := slogANewTest(t, 256, 2)
	var tokens [8][32]slogAToken
	var wg sync.WaitGroup
	start := make(chan struct{})
	for p := 0; p < 8; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			if !slogAStart(start) {
				return
			}
			for i := 0; i < 32; i++ {
				tok := c.reserve(slogAM())
				tokens[p][i] = tok
				c.enter(tok)
				c.returned(tok, slogAOK)
			}
		}(p)
	}
	close(start)
	slogAJoined(t, &wg)
	var seen [256]bool
	for p := range tokens {
		for _, tok := range tokens[p] {
			slogACheck(t, tok.mode == 1 && tok.index < 256 && !seen[tok.index] && tok.attempts <= 258, "SCA_CONCURRENT_UNIQUE")
			seen[tok.index] = true
		}
	}
	v := c.sealCut(0)
	slogACheck(t, c.summary(v).h == 256 && c.faultMask() == 0, "SCA_CONCURRENT_PREFIX")
	for i := 0; i < 256; i++ {
		r, ok := c.row(v, i)
		slogACheck(t, seen[i] && ok && r.state == slogAComplete, "SCA_CONCURRENT_COMPLETE_AFTER_HARNESS_JOIN")
	}
}

func TestSelfLogoutCollector20261021ConcurrentSeal(t *testing.T) {
	c := slogANewTest(t, 256, 2)
	var tokens [8][32]slogAToken
	var wg sync.WaitGroup
	start := make(chan struct{})
	for p := 0; p < 8; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			if !slogAStart(start) {
				return
			}
			for i := 0; i < 32; i++ {
				tok := c.reserve(slogAM())
				tokens[p][i] = tok
				c.enter(tok)
				c.returned(tok, slogAOther)
			}
		}(p)
	}
	close(start)
	sealed := c.seal()
	v := c.cut(0)
	before := c.summary(v)
	var old [256]slogARow
	for i := 0; i < int(before.h); i++ {
		old[i], _ = c.row(v, i)
	}
	slogAJoined(t, &wg)
	var seen [256]bool
	admitted := 0
	for p := range tokens {
		for _, tok := range tokens[p] {
			slogACheck(t, tok.attempts <= 258, "SCA_SEAL_ATTEMPTS")
			if tok.mode == 1 {
				slogACheck(t, tok.index < sealed.h && !seen[tok.index], "SCA_SEAL_UNIQUE")
				seen[tok.index], admitted = true, admitted+1
			} else {
				slogACheck(t, tok.mode == 2 && tok.reason == 1, "SCA_SEAL_LATE_ONLY")
			}
		}
	}
	slogACheck(t, sealed.ok && admitted == int(sealed.h) && c.gate.Load() == slogASeal|uint64(sealed.h), "SCA_SEAL_LINEARIZATION")
	slogACheck(t, c.summary(v) == before && c.faultMask() == 0, "SCA_CONCURRENT_OLD_HEADER")
	for i := 0; i < int(before.h); i++ {
		r, _ := c.row(v, i)
		slogACheck(t, r == old[i], "SCA_CONCURRENT_OLD_ROWS")
	}
	last := c.cut(1)
	for i := 0; i < int(sealed.h); i++ {
		r, _ := c.row(last, i)
		slogACheck(t, r.state == slogAComplete, "SCA_JOINED_TAIL_COMPLETE_ONLY_IN_CONTROL")
	}
}

func TestSelfLogoutCollector20261021Capacity(t *testing.T) {
	c := slogANewTest(t, slogACap, 1)
	for i := 0; i < slogACap; i++ {
		tok := c.reserve(slogAM())
		slogACheck(t, tok.mode == 1 && int(tok.index) == i && tok.attempts == 1, "SCA_FULL_PREFIX")
	}
	over := c.reserve(slogAM())
	slogACheck(t, over.mode == 2 && over.reason == 2 && !c.enter(over) && !c.returned(over, slogAOK), "SCA_8193_OVERFLOW")
	slogACheck(t, c.gate.Load() == slogACap && c.faultMask() == uint16(1)<<slogAOverflow && c.seal().h == slogACap, "SCA_NO_WRAP_OR_OVERWRITE")
}

func TestSelfLogoutCollector20261021CutCapacity(t *testing.T) {
	c := slogANewTest(t, 1, slogACuts)
	tok := c.reserve(slogAM())
	c.enter(tok)
	var first slogAViewToken
	for i := 0; i < slogACuts; i++ {
		v := c.cut(i)
		if i == 0 {
			first = v
		}
		slogACheck(t, c.summary(v).ready && c.summary(v).scanned == 1, "SCA_128_VIEWS")
	}
	before := c.summary(first)
	slogACheck(t, c.cut(slogACuts).owner == nil && c.cut(-1).owner == nil && c.cut(0).owner == nil, "SCA_VIEW_BOUND_AND_DUPLICATE")
	slogACheck(t, c.summary(first) == before && c.faultMask() == (uint16(1)<<slogACutRange|uint16(1)<<slogADuplicateCut), "SCA_VIEW_NOT_OVERWRITTEN")
}

func TestSelfLogoutCollector20261021InvalidInputs(t *testing.T) {
	base := slogAM()
	bad := []slogAMeta{
		{app: 0, conn: 1, tx: 1, pass: 1, kind: slogARollback},
		{app: 17, conn: 1, tx: 1, pass: 1, kind: slogARollback},
		{app: 1, conn: 0, tx: 1, pass: 1, kind: slogARollback},
		{app: 1, conn: 33, tx: 1, pass: 1, kind: slogARollback},
		{app: 1, conn: 1, tx: 0, pass: 1, kind: slogARollback},
		{app: 1, conn: 1, tx: 129, pass: 1, kind: slogARollback},
		{app: 1, conn: 1, tx: 1, pass: 0, kind: slogARollback},
		{app: 1, conn: 1, tx: 1, pass: 129, kind: slogARollback},
		{app: 1, conn: 1, tx: 1, kind: slogAClose},
		{app: 1, conn: 1, pass: 1, kind: slogAClose},
		{app: 1, conn: 1, kind: 99},
	}
	for _, m := range bad {
		c := slogANewTest(t, 1, 1)
		slogACheck(t, c.reserve(m).mode == 2 && c.gate.Load() == 0 && c.faultMask() == uint16(1)<<slogAIdentity, "SCA_BAD_SYMBOL")
	}
	c := slogANewTest(t, 1, 1)
	tok := c.reserve(base)
	c.enter(tok)
	slogACheck(t, !c.returned(tok, 255) && c.slots[0].returnPhase.Load() == slogAEmpty && c.faultMask() == uint16(1)<<slogAEnumFault, "SCA_BAD_RESULT_ENUM")
	slogACheck(t, newSlogCollectorA(0, 1) == nil && newSlogCollectorA(8193, 1) == nil && newSlogCollectorA(1, 0) == nil && newSlogCollectorA(1, 129) == nil, "SCA_BAD_CONSTRUCTION_BOUND")
}

func TestSelfLogoutCollector20261021DuplicatePublish(t *testing.T) {
	c := slogANewTest(t, 1, 1)
	tok := c.reserve(slogAM())
	slogACheck(t, !c.returned(tok, slogAOK) && c.enter(tok) && !c.enter(tok), "SCA_RETURN_BEFORE_ENTER_AND_DUPLICATE")
	var wg sync.WaitGroup
	for _, result := range []slogAResult{slogASQLite5, slogAOther} {
		wg.Add(1)
		go func(result slogAResult) { defer wg.Done(); c.returned(tok, result) }(result)
	}
	slogAJoined(t, &wg)
	r, _ := c.row(c.cut(0), 0)
	want := uint16(1)<<slogAReturnOrder | uint16(1)<<slogADuplicateEnter | uint16(1)<<slogADuplicateReturn
	slogACheck(t, r.state == slogAComplete && (r.result == slogASQLite5 || r.result == slogAOther) && c.faultMask() == want, "SCA_SOLE_RETURN_WRITER")
}

func TestSelfLogoutCollector20261021TokenOwnership(t *testing.T) {
	a, b := slogANewTest(t, 4, 1), slogANewTest(t, 4, 1)
	tok := a.reserve(slogAM())
	slogACheck(t, !b.enter(tok) && b.faultMask() == uint16(1)<<slogATokenFault && b.gate.Load() == 0, "SCA_FOREIGN_TOKEN")
	missing := tok
	missing.index = 1
	slogACheck(t, !a.enter(missing) && !a.enter(slogAToken{}), "SCA_UNRESERVED_AND_ZERO_TOKEN")
	slogACheck(t, a.enter(tok), "SCA_OWNER_STILL_PUBLISHES")
	wrongMeta := tok
	wrongMeta.meta.pass = 2
	slogACheck(t, !a.returned(wrongMeta, slogAOK) && a.returned(tok, slogAOK), "SCA_TOKEN_METADATA_MISMATCH")
	r, _ := a.row(a.cut(0), 0)
	slogACheck(t, r.state == slogAComplete && r.enter == tok.meta, "SCA_FOREIGN_NO_PAYLOAD_DAMAGE")
}

func TestSelfLogoutCollector20261021InvalidPhases(t *testing.T) {
	for _, phases := range [][2]uint32{{99, 0}, {0, 99}, {0, slogAReady}, {slogAWrite, slogAWrite}} {
		c := slogANewTest(t, 1, 1)
		c.reserve(slogAM())
		// Quiescent corruption witness only; no live writer/reset/reuse.
		c.slots[0].enterPhase.Store(phases[0])
		c.slots[0].returnPhase.Store(phases[1])
		v := c.cut(0)
		r, _ := c.row(v, 0)
		slogACheck(t, c.summary(v).scanned == 0 && r.state == slogANotScanned && c.faultMask() == uint16(1)<<slogAPhaseFault, "SCA_ILLEGAL_PHASE_HARD")
	}
	c := slogANewTest(t, 1, 1)
	c.gate.Store(uint64(1) << 20) // explicit corruption control, never a core path
	slogACheck(t, c.reserve(slogAM()).reason == 3 && !c.seal().ok, "SCA_BAD_GATE_REJECTED")
	c.cut(0)
	slogACheck(t, c.faultMask() == uint16(1)<<slogAGateFault, "SCA_BAD_GATE_HARD")
}

// Harness-only stand-in, NOT an interceptor or a core callback hook.
func slogASyntheticInvoke(c *slogCollectorA, delegate func() slogAResult) slogAResult {
	tok := c.reserve(slogAM())
	c.enter(tok)
	r := delegate()
	c.returned(tok, r)
	return r
}

func TestSelfLogoutCollector20261021HardFailureDoesNotIntercept(t *testing.T) {
	c := slogANewTest(t, 1, 1)
	c.reserve(slogAM())
	calls := 0
	r := slogASyntheticInvoke(c, func() slogAResult { calls++; return slogASQLite5 })
	slogACheck(t, calls == 1 && r == slogASQLite5 && c.faultMask() == uint16(1)<<slogAOverflow, "SCA_FAILURE_NOT_DELEGATE_POLICY")
	d := slogANewTest(t, 1, 1)
	sentinel := &struct{ marker int }{1}
	var got any
	func() {
		defer func() { got = recover() }() // harness only: core never recovers
		slogASyntheticInvoke(d, func() slogAResult { calls++; panic(sentinel) })
	}()
	row, _ := d.row(d.cut(0), 0)
	slogACheck(t, got == sentinel && calls == 2 && row.state == slogAOpen && d.faultMask() == 0, "SCA_PANIC_PROPAGATES_NO_RETURN")
}

func TestSelfLogoutCollector20261021StorageBounds(t *testing.T) {
	c := slogANewTest(t, slogACap, slogACuts)
	owned := unsafe.Sizeof(*c) + uintptr(len(c.slots))*unsafe.Sizeof(slogASlot{}) + uintptr(len(c.views))*unsafe.Sizeof(slogAView{}) + uintptr(len(c.rows))*unsafe.Sizeof(slogARow{})
	slogACheck(t, owned <= 16<<20 && len(c.rows) == 1048576 && len(c.slots) == 8192 && len(c.views) == 128, "SCA_OWNED_STORAGE_BOUND_NOT_RSS")
	d := slogANewTest(t, 4, 4)
	id := 0
	allocs := testing.AllocsPerRun(2, func() {
		tok := d.reserve(slogAM())
		d.enter(tok)
		d.returned(tok, slogAOK)
		d.cut(id)
		id++
	})
	slogACheck(t, allocs == 0 && d.faultMask() == 0, "SCA_CORE_NO_ALLOCATION_OR_GROWTH")
}

func TestSelfLogoutCollector20261021StepBounds(t *testing.T) {
	c := slogANewTest(t, 4, 4)
	for i := 0; i < 4; i++ {
		tok := c.reserve(slogAM())
		slogACheck(t, tok.attempts > 0 && tok.attempts <= c.n+2, "SCA_RESERVE_ITERATION_BOUND")
	}
	full := c.reserve(slogAM())
	sealed := c.seal()
	slogACheck(t, full.attempts <= c.n+2 && sealed.ok && sealed.attempts > 0 && sealed.attempts <= c.n+2 && c.seal().h == 4, "SCA_SEAL_ITERATION_BOUND")
	v := c.cut(0)
	slogACheck(t, c.summary(v).scanned == 4 && (2*(slogACap+2)+1) == 16389 && (2*slogACap+17) == 16401, "SCA_STATIC_ATOMIC_CALL_CEILINGS")
}
