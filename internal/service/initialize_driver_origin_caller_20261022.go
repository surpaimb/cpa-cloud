package service

// Independently authored for the 2026-10-22 caller-only Init-origin contract.
// Acquisition is solely the selected original store's public Driver getter.
// A separate B1 relay may deliver only to first Open entry; never to
// an App store, opener, Conn or transaction.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"sync"
)

type initializeOriginSelection uint8

const (
	initializeOriginNone    initializeOriginSelection = 0
	initializeOriginAuth    initializeOriginSelection = 1
	initializeOriginConfirm initializeOriginSelection = 2
)

type initializeOriginPhase uint8

const (
	initializeOriginNew       initializeOriginPhase = 0
	initializeOriginBound     initializeOriginPhase = 1
	initializeOriginEntered   initializeOriginPhase = 2
	initializeOriginStore     initializeOriginPhase = 3
	initializeOriginCaptured  initializeOriginPhase = 4
	initializeOriginPrepared  initializeOriginPhase = 5
	initializeOriginReturned  initializeOriginPhase = 6
	initializeOriginDelivered initializeOriginPhase = 7
)

type initializeOriginOutcome uint8

const (
	initializeOriginPending initializeOriginOutcome = 0
	initializeOriginSuccess initializeOriginOutcome = 1
	initializeOriginFailed  initializeOriginOutcome = 2
	initializeOriginUnknown initializeOriginOutcome = 3
)

type initializeOriginEvent struct {
	phase   initializeOriginPhase
	outcome initializeOriginOutcome
}

type initializeOriginView struct {
	selection                                                                           initializeOriginSelection
	phase                                                                               initializeOriginPhase
	outcome                                                                             initializeOriginOutcome
	invalid, attempted, storeBound, captured, prepared, callerSeen, delivered, admitted bool
	n                                                                                   uint8
	events                                                                              [7]initializeOriginEvent
}

// Plan storage exists before entry; attempt identity does not. All references
// stay private. A fixed value-only view never exposes these live objects.
type initializeOriginPlan struct {
	mu                              sync.Mutex
	selection                       initializeOriginSelection
	phase                           initializeOriginPhase
	outcome                         initializeOriginOutcome
	invalid                         bool
	attempt                         *initializeOriginAttempt
	store                           *store
	db                              *sql.DB
	implementation                  driver.Driver
	firstOpenDelivery               *initializeCallerDelivery
	firstOpenRelay                  *initializeFirstOpenRelay
	prepared, callerSeen, delivered bool
	n                               uint8
	events                          [7]initializeOriginEvent
}

type initializeOriginAttempt struct{ plan *initializeOriginPlan }

// Immutable caller-only result. It has no App, opener or public accessor.
type initializeCallerDelivery struct {
	attempt        *initializeOriginAttempt
	selection      initializeOriginSelection
	implementation driver.Driver
}

type initializeOriginBinding struct {
	context.Context
	plan *initializeOriginPlan
}

func newInitializeOriginPlan(selection initializeOriginSelection) *initializeOriginPlan {
	return &initializeOriginPlan{selection: selection, invalid: selection != initializeOriginAuth && selection != initializeOriginConfirm}
}

func (p *initializeOriginPlan) reject() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.invalid = true
	p.mu.Unlock()
}

func (p *initializeOriginPlan) appendLocked() bool {
	if int(p.n) >= len(p.events) {
		p.invalid = true
		return false
	}
	p.events[p.n] = initializeOriginEvent{phase: p.phase, outcome: p.outcome}
	p.n++
	return true
}

func withInitializeOrigin(ctx context.Context, p *initializeOriginPlan) context.Context {
	if p == nil {
		return ctx
	}
	if old, ok := ctx.(initializeOriginBinding); ok {
		old.plan.reject()
		p.reject()
		return ctx
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.invalid || p.phase != initializeOriginNew {
		p.invalid = true
		return ctx
	}
	p.phase = initializeOriginBound
	if !p.appendLocked() {
		return ctx
	}
	return initializeOriginBinding{Context: ctx, plan: p}
}

// Only the actual WithConfig entry calls this in production source. No Context
// method is evaluated; unwrap to the exact original business Context object.
func takeInitializeOrigin(ctx context.Context) (context.Context, *initializeOriginAttempt) {
	b, ok := ctx.(initializeOriginBinding)
	if !ok {
		return ctx, nil
	}
	if b.plan == nil {
		return b.Context, nil
	}
	p := b.plan
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.invalid || p.phase != initializeOriginBound || p.attempt != nil {
		p.invalid = true
		return b.Context, nil
	}
	a := &initializeOriginAttempt{plan: p}
	p.attempt = a
	p.phase = initializeOriginEntered
	if !p.appendLocked() {
		return b.Context, nil
	}
	return b.Context, a
}

func (a *initializeOriginAttempt) bindOriginalStore(s *store) bool {
	if a == nil || a.plan == nil {
		return false
	}
	p := a.plan
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.invalid || p.attempt != a || p.phase != initializeOriginEntered || s == nil || s.db == nil {
		p.invalid = true
		return false
	}
	p.store, p.db = s, s.db
	p.phase = initializeOriginStore
	return p.appendLocked()
}

// Classification only: no private field/registry lookup or implementation
// acquisition through reflection, and no driver method is invoked.
func initializeOriginNilDriver(d driver.Driver) bool {
	if d == nil {
		return true
	}
	v := reflect.ValueOf(d)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func (a *initializeOriginAttempt) captureFromStore(s *store, d driver.Driver) {
	if a == nil || a.plan == nil {
		return
	}
	p := a.plan
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.invalid || p.attempt != a || p.phase != initializeOriginStore || s == nil || s != p.store || s.db != p.db || initializeOriginNilDriver(d) {
		p.invalid = true
		return
	}
	p.implementation = d
	p.phase = initializeOriginCaptured
	p.appendLocked()
}

// A prepared nil result is not an actual function return or cleanup outcome.
// Panic/non-return cannot reach the separately audited caller association.
func (a *initializeOriginAttempt) nilResultPrepared() {
	if a == nil || a.plan == nil {
		return
	}
	p := a.plan
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.invalid || p.attempt != a || p.phase != initializeOriginCaptured {
		p.invalid = true
		return
	}
	p.prepared = true
	p.phase = initializeOriginPrepared
	p.appendLocked()
}

func (p *initializeOriginPlan) attemptToken() *initializeOriginAttempt {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attempt
}

// Actual caller source must pass its real Initialize result after that call.
// No raw error is retained/formatted; it cannot change the business return.
func (p *initializeOriginPlan) callerReturned(a *initializeOriginAttempt, actualErr error) *initializeCallerDelivery {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.invalid || a == nil || p.attempt != a || a.plan != p || p.callerSeen {
		p.invalid = true
		return nil
	}
	p.callerSeen = true
	p.outcome = initializeOriginUnknown
	if actualErr != nil {
		p.outcome = initializeOriginFailed
	} else if p.phase == initializeOriginPrepared && p.prepared && p.store != nil && p.db != nil && p.implementation != nil {
		p.outcome = initializeOriginSuccess
	}
	p.phase = initializeOriginReturned
	if !p.appendLocked() {
		return nil
	}
	if p.outcome != initializeOriginSuccess {
		p.invalid = true
		return nil
	}
	p.phase = initializeOriginDelivered
	if !p.appendLocked() {
		return nil
	}
	p.delivered = true
	delivery := &initializeCallerDelivery{attempt: a, selection: p.selection, implementation: p.implementation}
	p.firstOpenDelivery = delivery
	return delivery
}

func (p *initializeOriginPlan) view() initializeOriginView {
	if p == nil {
		return initializeOriginView{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return initializeOriginView{
		selection: p.selection, phase: p.phase, outcome: p.outcome, invalid: p.invalid,
		attempted: p.attempt != nil, storeBound: p.store != nil && p.db != nil,
		captured: p.implementation != nil, prepared: p.prepared, callerSeen: p.callerSeen,
		delivered: p.delivered, admitted: p.delivered && !p.invalid,
		n: p.n, events: p.events,
	}
}
