package service

// Independently authored from the 2026-10-22 first-App owner-only contract.
// No external implementation, new dependency, protocol or business decision.

import (
	"context"
	"sync"
)

type expiryOwnerSelection uint8

const (
	expiryOwnerUnselected   expiryOwnerSelection = 0
	expiryOwnerAuthFirst    expiryOwnerSelection = 1
	expiryOwnerConfirmFirst expiryOwnerSelection = 2
)

type expiryOwnerReturn uint8

const (
	expiryOwnerPending   expiryOwnerReturn = 0
	expiryOwnerSucceeded expiryOwnerReturn = 1
	expiryOwnerFailed    expiryOwnerReturn = 2
	expiryOwnerUnknown   expiryOwnerReturn = 3
)

type expiryOwnerEventKind uint8

const (
	expiryOwnerEntry       expiryOwnerEventKind = 1
	expiryOwnerAppBuilt    expiryOwnerEventKind = 2
	expiryOwnerWorkerBuilt expiryOwnerEventKind = 3
	expiryOwnerStarted     expiryOwnerEventKind = 4
	expiryOwnerReturned    expiryOwnerEventKind = 5
)

type expiryOwnerEvent struct {
	kind        expiryOwnerEventKind
	returnState expiryOwnerReturn
}

// A value-only view deliberately has no App, worker, context or capability.
type expiryOwnerView struct {
	selection   expiryOwnerSelection
	returned    expiryOwnerReturn
	invalid     bool
	claimed     bool
	appBound    bool
	workerBound bool
	started     bool
	accepted    bool
	n           uint8
	events      [5]expiryOwnerEvent
}

// One plan has exactly one invocation slot and five event slots. No global
// registry or counter exists. Never copy a plan after first use.
type expiryInvocationPlan struct {
	mu        sync.Mutex
	selection expiryOwnerSelection
	owner     *expiryInvocationOwner
	app       *App
	worker    *subscriptionExpiryWorker
	started   bool
	returned  expiryOwnerReturn
	invalid   bool
	n         uint8
	events    [5]expiryOwnerEvent
}

// Non-zero-size, private capability allocated only at the real Open entry.
// Its plan association is immutable. Pointer values are never output or used
// to derive a source label; equality only validates the actual object chain.
type expiryInvocationOwner struct {
	plan *expiryInvocationPlan
}

func newExpiryInvocationPlan(selection expiryOwnerSelection) *expiryInvocationPlan {
	return &expiryInvocationPlan{selection: selection, invalid: selection != expiryOwnerAuthFirst && selection != expiryOwnerConfirmFirst}
}

// Extend the existing startup carrier, not an additional Context layer. Open's
// unchanged two-result extractor still restores the original business context.
func withExpiryInvocationOwner(ctx context.Context, plan *expiryInvocationPlan) context.Context {
	if plan == nil {
		return ctx
	}
	if binding, ok := ctx.(expiryOpenBinding); ok {
		if binding.ownerPlan != nil {
			binding.ownerPlan.reject()
			if binding.ownerPlan != plan {
				plan.reject()
			}
			return ctx
		}
		binding.ownerPlan = plan
		return binding
	}
	return expiryOpenBinding{Context: ctx, ownerPlan: plan}
}

func (p *expiryInvocationPlan) reject() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.invalid = true
	p.mu.Unlock()
}

func beginExpiryInvocationOwner(ctx context.Context) *expiryInvocationOwner {
	binding, ok := ctx.(expiryOpenBinding)
	if !ok || binding.ownerPlan == nil {
		return nil
	}
	p := binding.ownerPlan
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.invalid || p.owner != nil {
		p.invalid = true
		return nil
	}
	owner := &expiryInvocationOwner{plan: p}
	p.owner = owner
	p.appendLocked(expiryOwnerEntry)
	return owner
}

func (p *expiryInvocationPlan) appendLocked(kind expiryOwnerEventKind) {
	if int(p.n) >= len(p.events) {
		p.invalid = true
		return
	}
	p.events[p.n] = expiryOwnerEvent{kind: kind, returnState: p.returned}
	p.n++
}

func (o *expiryInvocationOwner) appConstructed(app *App) {
	if o == nil || o.plan == nil {
		return
	}
	p := o.plan
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.invalid || p.owner != o || p.app != nil || app == nil || app.expiryOwner != o || p.returned != expiryOwnerPending {
		p.invalid = true
		return
	}
	p.app = app
	p.appendLocked(expiryOwnerAppBuilt)
}

func (o *expiryInvocationOwner) workerConstructed(app *App, worker *subscriptionExpiryWorker) {
	if o == nil || o.plan == nil {
		return
	}
	p := o.plan
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.invalid || p.owner != o || p.app == nil || p.app != app || app.expiryOwner != o || worker == nil || worker.owner != o || p.worker != nil || p.returned != expiryOwnerPending {
		p.invalid = true
		return
	}
	p.worker = worker
	p.appendLocked(expiryOwnerWorkerBuilt)
}

func (o *expiryInvocationOwner) goroutineStarted(worker *subscriptionExpiryWorker) {
	if o == nil || o.plan == nil {
		return
	}
	p := o.plan
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.invalid || p.owner != o || p.worker == nil || p.worker != worker || worker.owner != o || p.started {
		p.invalid = true
		return
	}
	p.started = true
	p.appendLocked(expiryOwnerStarted)
}

func (p *expiryInvocationPlan) capability() *expiryInvocationOwner {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.owner
}

// Only the original caller invokes this after its actual Open return. This
// records association, not a new owner and not a rewrite of previous events.
func (p *expiryInvocationPlan) callerReturned(owner *expiryInvocationOwner, app *App, err error) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.invalid || owner == nil || p.owner != owner || owner.plan != p || p.returned != expiryOwnerPending {
		p.invalid = true
		return
	}
	state := expiryOwnerUnknown
	if err != nil && app == nil {
		state = expiryOwnerFailed
	} else if err == nil && app != nil && p.app == app && app.expiryOwner == owner && p.worker != nil && app.subscriptionExpiry == p.worker {
		state = expiryOwnerSucceeded
	}
	p.returned = state
	p.appendLocked(expiryOwnerReturned)
	if state == expiryOwnerUnknown {
		p.invalid = true
	}
}

func (p *expiryInvocationPlan) view() expiryOwnerView {
	if p == nil {
		return expiryOwnerView{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return expiryOwnerView{
		selection: p.selection, returned: p.returned, invalid: p.invalid,
		claimed: p.owner != nil, appBound: p.app != nil, workerBound: p.worker != nil,
		started: p.started, accepted: !p.invalid && p.n == 5 && p.started && p.returned == expiryOwnerSucceeded,
		n: p.n, events: p.events,
	}
}
