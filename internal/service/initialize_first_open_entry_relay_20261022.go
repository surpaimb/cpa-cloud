package service

// Independently authored B1 entry-reference metadata. No opener/store/Conn/Tx
// use, driver method, Context method, IO, callback or business result is added.
import "sync"

type firstOpenEntryPhase uint8

const (
	firstOpenEntryNone     firstOpenEntryPhase = 0
	firstOpenEntryPrebound firstOpenEntryPhase = 1
	firstOpenEntryArmed    firstOpenEntryPhase = 2
	firstOpenEntryReceived firstOpenEntryPhase = 3
)

type firstOpenEntryEvent uint8

const (
	firstOpenEntryArmEvent     firstOpenEntryEvent = 1
	firstOpenEntryReceiveEvent firstOpenEntryEvent = 2
)

type firstOpenEntryView struct {
	selection                                                      initializeOriginSelection
	phase                                                          firstOpenEntryPhase
	invalid, targetBound, armed, received, referenceOnly, admitted bool
	n                                                              uint8
	events                                                         [2]firstOpenEntryEvent
}

// The immutable origin/target pair is set before Initialize. No receipt is
// allowed to teach this object its expected target. References are not output.
type initializeFirstOpenRelay struct {
	mu          sync.Mutex
	origin      *initializeOriginPlan
	target      *expiryInvocationPlan
	selection   initializeOriginSelection
	delivery    *initializeCallerDelivery
	owner       *expiryInvocationOwner
	phase       firstOpenEntryPhase
	invalid     bool
	targetBound bool
	armed       bool
	received    bool
	n           uint8
	events      [2]firstOpenEntryEvent
}

func firstOpenEntrySelection(init initializeOriginSelection, owner expiryOwnerSelection) bool {
	return init == initializeOriginAuth && owner == expiryOwnerAuthFirst ||
		init == initializeOriginConfirm && owner == expiryOwnerConfirmFirst
}

func (r *initializeFirstOpenRelay) reject() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.invalid = true
	r.mu.Unlock()
}

func firstOpenRelayFor(p *initializeOriginPlan) *initializeFirstOpenRelay {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	r := p.firstOpenRelay
	p.mu.Unlock()
	return r
}

// Original fixture startup calls this before its actual Initialize. Old plan
// locks are released before installing into the other plan; no lock cycle.
func prepareFirstOpenRelay(p *initializeOriginPlan, target *expiryInvocationPlan) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if prior := p.firstOpenRelay; prior != nil {
		p.mu.Unlock()
		prior.reject()
		return
	}
	r := &initializeFirstOpenRelay{origin: p, target: target, selection: p.selection}
	p.firstOpenRelay = r
	ready := !p.invalid && p.phase == initializeOriginNew && p.attempt == nil && p.firstOpenDelivery == nil
	p.mu.Unlock()
	if !ready || target == nil {
		r.reject()
		return
	}
	target.mu.Lock()
	if target.invalid || target.owner != nil || target.returned != expiryOwnerPending || target.firstOpenRelay != nil || !firstOpenEntrySelection(r.selection, target.selection) {
		target.mu.Unlock()
		r.reject()
		return
	}
	target.firstOpenRelay = r
	r.mu.Lock()
	r.targetBound = true
	r.phase = firstOpenEntryPrebound
	r.mu.Unlock()
	target.mu.Unlock()
}

// Only canonical publication from the audited actual caller may arm. No old
// Init events are appended or old owner state modified by this association.
func armFirstOpenRelay(p *initializeOriginPlan, delivery *initializeCallerDelivery) {
	if p == nil {
		return
	}
	p.mu.Lock()
	r := p.firstOpenRelay
	valid := !p.invalid && p.phase == initializeOriginDelivered && p.outcome == initializeOriginSuccess &&
		p.prepared && p.callerSeen && p.delivered && p.attempt != nil && p.attempt.plan == p &&
		p.store != nil && p.db != nil && p.store.db == p.db && p.implementation != nil &&
		delivery != nil && p.firstOpenDelivery == delivery && delivery.attempt == p.attempt &&
		delivery.selection == p.selection && delivery.implementation != nil
	p.mu.Unlock()
	if r == nil {
		return
	}
	if !valid || r.origin != p || r.target == nil {
		r.reject()
		return
	}
	target := r.target
	target.mu.Lock()
	if target.invalid || target.owner != nil || target.returned != expiryOwnerPending || target.firstOpenRelay != r || !firstOpenEntrySelection(r.selection, target.selection) {
		target.mu.Unlock()
		r.reject()
		return
	}
	r.mu.Lock()
	if r.invalid || !r.targetBound || r.phase != firstOpenEntryPrebound || r.armed || r.received || r.delivery != nil || r.owner != nil || r.n != 0 {
		r.invalid = true
	} else {
		r.delivery = delivery
		r.phase = firstOpenEntryArmed
		if r.appendLocked(firstOpenEntryArmEvent) {
			r.armed = true
		}
	}
	r.mu.Unlock()
	target.mu.Unlock()
}

func (r *initializeFirstOpenRelay) appendLocked(event firstOpenEntryEvent) bool {
	if int(r.n) >= len(r.events) {
		r.invalid = true
		return false
	}
	r.events[r.n] = event
	r.n++
	return true
}

// Called only inside the existing beginExpiryInvocationOwner while its target
// lock is held, after that function installed its freshly allocated capability.
// Never reacquire either old plan lock here. No App has been constructed yet.
func (r *initializeFirstOpenRelay) receiveAtEntry(target *expiryInvocationPlan, owner *expiryInvocationOwner) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.invalid || r.phase != firstOpenEntryArmed || !r.armed || r.received || r.owner != nil || r.delivery == nil || r.n != 1 || r.events[0] != firstOpenEntryArmEvent ||
		target == nil || r.target != target || target.firstOpenRelay != r || target.invalid ||
		owner == nil || target.owner != owner || owner.plan != target || target.returned != expiryOwnerPending ||
		target.app != nil || target.worker != nil || target.started || target.n != 1 || target.events[0] != (expiryOwnerEvent{expiryOwnerEntry, expiryOwnerPending}) ||
		!firstOpenEntrySelection(r.selection, target.selection) {
		r.invalid = true
		return
	}
	r.phase = firstOpenEntryReceived
	if !r.appendLocked(firstOpenEntryReceiveEvent) {
		return
	}
	r.owner = owner
	r.received = true
}

func (r *initializeFirstOpenRelay) view() firstOpenEntryView {
	if r == nil {
		return firstOpenEntryView{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return firstOpenEntryView{
		selection: r.selection, phase: r.phase, invalid: r.invalid,
		targetBound: r.targetBound, armed: r.armed, received: r.received,
		referenceOnly: r.received, admitted: !r.invalid && r.received && r.phase == firstOpenEntryReceived && r.n == 2,
		n: r.n, events: r.events,
	}
}
