package service

// Independently authored owner-only source-contract control. No App/DB is
// constructed by this test. See the 2026-10-22 owner-only contract.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/format"
	"os"
	"regexp"
	"strings"
	"testing"

	"cpacloud.local/server/internal/financial"
)

func eioSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal("FAPP_SOURCE_READ")
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// Helpers in this new file keep the original Gate4 cleanup literal and its
// global arm/route/cut/report/newSlogPrequery multiplicities unchanged.
func firstOwnerBoundarySelection(scenario string) expiryOwnerSelection {
	if scenario == "confirmation-query-error" {
		return expiryOwnerConfirmFirst
	}
	return expiryOwnerUnselected
}

func (f *slog21Fixture) installFirstOwner(t *testing.T, selection []expiryOwnerSelection) {
	if len(selection) == 0 || len(selection) == 1 && selection[0] == expiryOwnerUnselected {
		return
	}
	f.firstOwner = newExpiryInvocationPlan(selection[0])
	if len(selection) != 1 {
		f.firstOwner.reject()
	}
	// Registered before the old constructor cleanup: LIFO runs this last.
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("FAPP_OWNER_ONLY_NOT_ACCEPTED_PRIOR_FAILURE")
			return
		}
		v := f.firstOwner.view()
		if !v.accepted || !f.firstOwnerAttempted || !v.claimed || !v.appBound || !v.workerBound || !v.started {
			t.Error("FAPP_OWNER_ONLY_REJECTED")
			return
		}
		switch v.selection {
		case expiryOwnerAuthFirst:
			t.Log("FAPP_OWNER_ONLY_ACCEPTED_AUTH_FIRST_FINAL_CLEAN")
		case expiryOwnerConfirmFirst:
			t.Log("FAPP_OWNER_ONLY_ACCEPTED_CONFIRM_FIRST_REOPEN")
		default:
			t.Error("FAPP_OWNER_ONLY_SELECTION")
		}
	})
}

func (f *slog21Fixture) firstOwnerStartup(ctx context.Context) (context.Context, *expiryInvocationPlan) {
	if f.firstOwner == nil || f.firstOwnerAttempted {
		return ctx, nil
	}
	f.firstOwnerAttempted = true
	return withExpiryInvocationOwner(ctx, f.firstOwner), f.firstOwner
}

func (f *slog21Fixture) firstOwnerReturned(plan *expiryInvocationPlan, app *App, err error) {
	if plan != nil {
		if plan != f.firstOwner {
			f.firstOwner.reject()
			plan.reject()
			return
		}
		plan.callerReturned(plan.capability(), app, err)
	}
}

func eioHashSource(src string) string {
	b, err := format.Source([]byte(src))
	if err != nil {
		return "parse-error"
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func eioAudit(app, worker, fixture string) string {
	if !strings.Contains(app, "func Open(ctx context.Context, cfg Config) (*App, error) {\n\towner := beginExpiryInvocationOwner(ctx)\n\tctx, expiryObserver := takeExpiryOpenObserver(ctx)") {
		return "FAPP_SOURCE_OPEN_ENTRY_MISSING"
	}
	if !strings.Contains(app, "\n\t}\n\towner.appConstructed(app)\n\topened := false") ||
		!strings.Contains(worker, ", owner: app.expiryOwner}\n\tworker.owner.workerConstructed(app, worker)\n\tworker.expiryPoint") ||
		!strings.Contains(worker, "\n\t\t}()\n\t\tworker.owner.goroutineStarted(worker)\n\t\tworker.expiryPoint") {
		return "FAPP_SOURCE_ACTUAL_CHAIN"
	}
	strip := func(src string, exact []string, fields []string) string {
		for _, s := range exact {
			if strings.Count(src, s) != 1 {
				return "nonunique-addition"
			}
			src = strings.Replace(src, s, "", 1)
		}
		for _, field := range fields {
			r := regexp.MustCompile("(?m)^[\\t ]*" + field + "[\\t ]+[^\\n]+\\n")
			if len(r.FindAllString(src, -1)) != 1 {
				return "nonunique-field"
			}
			src = r.ReplaceAllString(src, "")
		}
		return src
	}
	oldApp := strip(app, []string{
		"\towner := beginExpiryInvocationOwner(ctx)\n",
		"\towner.appConstructed(app)\n",
	}, []string{"expiryOwner", "ownerPlan"})
	oldApp = regexp.MustCompile("(?m)^[\\t ]*expiryOwner:[^\\n]+\\n").ReplaceAllString(oldApp, "")
	if eioHashSource(oldApp) != "82b469869a5ca87decd34e98f4acad477163c10da87cb638e783e66619876944" {
		return "FAPP_SOURCE_APP_BOUNDARY"
	}
	oldWorker := strip(worker, []string{
		", owner: app.expiryOwner",
		"\tworker.owner.workerConstructed(app, worker)\n",
		"\t\tworker.owner.goroutineStarted(worker)\n",
	}, []string{"owner"})
	if eioHashSource(oldWorker) != "8e6aca505ba606c2d67f3163fe698b9b0ec7d11f752242aac96ef5c7dfeae6e0" {
		return "FAPP_SOURCE_WORKER_BOUNDARY"
	}
	fixture, originLabel := projectInitializeOriginCallerFixture(fixture)
	if originLabel != "" {
		return originLabel
	}
	oldFixture := strip(fixture, []string{
		"\tf.installFirstOwner(t, selection)\n",
		"\tf.firstOwnerReturned(ownerPlan, a, err)\n",
	}, []string{"firstOwner", "firstOwnerAttempted"})
	for _, pair := range [][2]string{
		{"func newSlog21(t *testing.T, selection ...expiryOwnerSelection)", "func newSlog21(t *testing.T)"},
		{"startup, ownerPlan := f.firstOwnerStartup(withExpiryOpenObserver(ctx, expiryRecorder.capture))", "startup := withExpiryOpenObserver(ctx, expiryRecorder.capture)"},
		{"newSlog21(t, firstOwnerBoundarySelection(scenario))", "newSlog21(t)"},
		{"newSlog21(t, expiryOwnerAuthFirst)", "newSlog21(t)"},
	} {
		if strings.Count(oldFixture, pair[0]) != 1 {
			return "FAPP_SOURCE_FIXTURE_SELECTION"
		}
		oldFixture = strings.Replace(oldFixture, pair[0], pair[1], 1)
	}
	b, err := format.Source([]byte(oldFixture))
	if err != nil {
		return "FAPP_SOURCE_FIXTURE_PARSE"
	}
	normalized := regexp.MustCompile("line: +[0-9]+").ReplaceAllString(string(b), "line: 0")
	if fmt.Sprintf("%x", sha256.Sum256([]byte(normalized))) != "e09d589f8ab5be33e5937e0ce31d7336bd7e70d614ae12a615712120058fad22" {
		return "FAPP_SOURCE_FIXTURE_BOUNDARY"
	}
	return ""
}

func TestExpiryInvocationOwner20261022Source(t *testing.T) {
	app, worker, fixture := eioSource(t, "app.go"), eioSource(t, "subscription_expiry_worker.go"), eioSource(t, "self_logout_confirmation_20261021_test.go")
	if label := eioAudit(app, worker, fixture); label != "" {
		t.Fatal(label)
	}
	// Invoke the inherited, unmodified AST guard, including real line anchors.
	fs, parsed := spqiParse(t, []byte(fixture))
	if spqiAudit(fs, parsed) != "" {
		t.Fatal("FAPP_INHERITED_GATE4_GUARD")
	}
	for _, mutation := range []struct{ name, app, worker string }{
		{"drop-app-worker-source", app, strings.Replace(worker, "owner: app.expiryOwner", "owner: nil", 1)},
		{"premature-return", strings.Replace(app, "\topened := false", "\towner.plan.callerReturned(owner, app, nil)\n\topened := false", 1), worker},
		{"replace-background-parent", app, strings.Replace(worker, "context.WithCancel(context.Background())", "context.WithCancel(context.TODO())", 1)},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			if eioAudit(mutation.app, mutation.worker, fixture) == "" {
				t.Fatal("FAPP_SOURCE_MUTATION_ACCEPTED")
			}
		})
	}
}

// Synthetic metadata only. These App/worker values have no store, DB, timer,
// goroutine or service. Only the two old selected cases exercise real wiring.
func eioObjects() (*expiryInvocationPlan, *expiryInvocationOwner, *App, *subscriptionExpiryWorker) {
	p := newExpiryInvocationPlan(expiryOwnerAuthFirst)
	o := beginExpiryInvocationOwner(withExpiryInvocationOwner(context.Background(), p))
	a := &App{expiryOwner: o}
	o.appConstructed(a)
	w := &subscriptionExpiryWorker{owner: o}
	o.workerConstructed(a, w)
	a.subscriptionExpiry = w
	return p, o, a, w
}

func TestExpiryInvocationOwner20261022StateValues(t *testing.T) {
	// Independent expected numbers, not values generated from the same enum.
	if [3]uint8{uint8(expiryOwnerUnselected), uint8(expiryOwnerAuthFirst), uint8(expiryOwnerConfirmFirst)} != [3]uint8{0, 1, 2} ||
		[4]uint8{uint8(expiryOwnerPending), uint8(expiryOwnerSucceeded), uint8(expiryOwnerFailed), uint8(expiryOwnerUnknown)} != [4]uint8{0, 1, 2, 3} ||
		[5]uint8{uint8(expiryOwnerEntry), uint8(expiryOwnerAppBuilt), uint8(expiryOwnerWorkerBuilt), uint8(expiryOwnerStarted), uint8(expiryOwnerReturned)} != [5]uint8{1, 2, 3, 4, 5} {
		t.Fatal("FAPP_STATE_VALUES")
	}
	var p *expiryInvocationPlan
	if v := p.view(); v != (expiryOwnerView{}) || v.accepted || uint8(v.returned) != 0 {
		t.Fatal("FAPP_ZERO_NOT_PENDING")
	}
}

func TestExpiryInvocationOwner20261022Orders(t *testing.T) {
	for _, startFirst := range []bool{true, false} {
		name := "return-before-worker"
		if startFirst {
			name = "worker-before-return"
		}
		t.Run(name, func(t *testing.T) {
			p, o, a, w := eioObjects()
			initial := p.view()
			if initial.n != 3 || initial.returned != expiryOwnerPending || initial.accepted {
				t.Fatal("FAPP_PENDING_CONSTRUCTION")
			}
			if startFirst {
				o.goroutineStarted(w)
			} else {
				p.callerReturned(o, a, nil)
			}
			before := p.view()
			if startFirst {
				p.callerReturned(o, a, nil)
			} else {
				o.goroutineStarted(w)
			}
			after := p.view()
			if !after.accepted || after.invalid || after.n != 5 || !after.claimed || !after.appBound || !after.workerBound || !after.started || after.returned != expiryOwnerSucceeded {
				t.Fatal("FAPP_LEGAL_ORDER_REJECTED")
			}
			for i := uint8(0); i < before.n; i++ {
				if before.events[i] != after.events[i] || i < 3 && after.events[i].returnState != expiryOwnerPending {
					t.Fatal("FAPP_RETROACTIVE_REWRITE")
				}
			}
			if startFirst && after.events[3] != (expiryOwnerEvent{expiryOwnerStarted, expiryOwnerPending}) ||
				!startFirst && after.events[4] != (expiryOwnerEvent{expiryOwnerStarted, expiryOwnerSucceeded}) {
				t.Fatal("FAPP_START_RETURN_STATE")
			}
		})
	}
}

func TestExpiryInvocationOwner20261022Rejected(t *testing.T) {
	for _, scenario := range []string{"missing-capability", "foreign-capability", "foreign-app", "foreign-worker", "duplicate-app", "duplicate-worker", "duplicate-start", "duplicate-return", "missing-worker-source"} {
		t.Run(scenario, func(t *testing.T) {
			p, o, a, w := eioObjects()
			switch scenario {
			case "missing-capability":
				p.callerReturned(nil, a, nil)
			case "foreign-capability":
				_, foreign, _, _ := eioObjects()
				p.callerReturned(foreign, a, nil)
			case "foreign-app":
				p.callerReturned(o, &App{expiryOwner: o, subscriptionExpiry: w}, nil)
			case "foreign-worker":
				o.goroutineStarted(&subscriptionExpiryWorker{owner: o})
			case "duplicate-app":
				o.appConstructed(a)
			case "duplicate-worker":
				o.workerConstructed(a, w)
			case "duplicate-start":
				o.goroutineStarted(w)
				o.goroutineStarted(w)
			case "duplicate-return":
				p.callerReturned(o, a, nil)
				p.callerReturned(o, a, nil)
			case "missing-worker-source":
				w.owner = nil // Synthetic loss, not a change to a real worker.
				o.goroutineStarted(w)
			}
			before := p.view()
			o.goroutineStarted(w)
			p.callerReturned(o, a, nil)
			after := p.view()
			if !before.invalid || after.accepted || !after.invalid || before != after {
				t.Fatal("FAPP_REJECTION_NOT_LATCHED")
			}
		})
	}
	t.Run("duplicate-entry", func(t *testing.T) {
		p := newExpiryInvocationPlan(expiryOwnerAuthFirst)
		startup := withExpiryInvocationOwner(context.Background(), p)
		first := beginExpiryInvocationOwner(startup)
		before := p.view()
		if first == nil || beginExpiryInvocationOwner(startup) != nil || !p.view().invalid || p.view().events != before.events {
			t.Fatal("FAPP_REUSED_INVOCATION")
		}
	})
	t.Run("duplicate-carrier", func(t *testing.T) {
		p, foreign := newExpiryInvocationPlan(expiryOwnerAuthFirst), newExpiryInvocationPlan(expiryOwnerConfirmFirst)
		ctx := withExpiryInvocationOwner(context.Background(), p)
		got, ok := withExpiryInvocationOwner(ctx, foreign).(expiryOpenBinding)
		if !ok || got.Context != context.Background() || got.ownerPlan != p || !p.view().invalid || !foreign.view().invalid || p.capability() != nil || foreign.capability() != nil {
			t.Fatal("FAPP_CARRIER_OVERWRITE")
		}
	})
	t.Run("invalid-selection", func(t *testing.T) {
		for _, selection := range []expiryOwnerSelection{0, 3, 255} {
			p := newExpiryInvocationPlan(selection)
			if beginExpiryInvocationOwner(withExpiryInvocationOwner(context.Background(), p)) != nil || !p.view().invalid || p.view().n != 0 {
				t.Fatal("FAPP_INVALID_SELECTION")
			}
		}
	})
	t.Run("capacity-no-overwrite", func(t *testing.T) {
		p, _, _, _ := eioObjects()
		p.n = 5 // Synthetic exhausted unpublished storage.
		before := p.events
		p.appendLocked(expiryOwnerStarted)
		if !p.invalid || p.n != 5 || p.events != before {
			t.Fatal("FAPP_CAPACITY_OVERWRITE")
		}
	})
}

func TestExpiryInvocationOwner20261022ReturnStates(t *testing.T) {
	t.Run("failed-before-app", func(t *testing.T) {
		p := newExpiryInvocationPlan(expiryOwnerAuthFirst)
		o := beginExpiryInvocationOwner(withExpiryInvocationOwner(context.Background(), p))
		p.callerReturned(o, nil, errors.New("synthetic failure"))
		v := p.view()
		if v.returned != expiryOwnerFailed || v.accepted || v.appBound || v.workerBound || v.n != 2 || v.events[0].returnState != expiryOwnerPending {
			t.Fatal("FAPP_FAILED_OPEN_SUCCEEDED")
		}
	})
	t.Run("failed-after-construction", func(t *testing.T) {
		p, o, _, w := eioObjects()
		o.goroutineStarted(w)
		before := p.view()
		p.callerReturned(o, nil, errors.New("synthetic failure"))
		v := p.view()
		if v.returned != expiryOwnerFailed || v.accepted || !v.appBound || !v.workerBound || !v.started || v.n != 5 {
			t.Fatal("FAPP_FAILED_CONSTRUCTION_SUCCEEDED")
		}
		for i := uint8(0); i < before.n; i++ {
			if before.events[i] != v.events[i] {
				t.Fatal("FAPP_FAILURE_REWROTE_SOURCE")
			}
		}
	})
	for _, scenario := range []string{"pending", "nil-nil", "premature-success", "missing-start"} {
		t.Run(scenario, func(t *testing.T) {
			p := newExpiryInvocationPlan(expiryOwnerAuthFirst)
			o := beginExpiryInvocationOwner(withExpiryInvocationOwner(context.Background(), p))
			switch scenario {
			case "nil-nil":
				p.callerReturned(o, nil, nil)
			case "premature-success":
				p.callerReturned(o, &App{expiryOwner: o}, nil)
			case "missing-start":
				p, o, a, _ := eioObjects()
				p.callerReturned(o, a, nil)
				if p.view().accepted || p.view().started || p.view().n != 4 {
					t.Fatal("FAPP_MISSING_START_ACCEPTED")
				}
				return
			}
			v := p.view()
			if v.accepted || scenario == "pending" && (v.returned != expiryOwnerPending || v.invalid) || scenario != "pending" && (v.returned != expiryOwnerUnknown || !v.invalid) {
				t.Fatal("FAPP_UNPROVED_RETURN_SUCCEEDED")
			}
		})
	}
}

func TestExpiryInvocationOwner20261022DefaultAndCarrier(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.WithValue(context.Background(), struct{ marker bool }{true}, 7))
	defer cancel(nil)
	if n := testing.AllocsPerRun(20, func() {
		if withExpiryInvocationOwner(ctx, nil) != ctx || beginExpiryInvocationOwner(ctx) != nil {
			panic("FAPP_DEFAULT_CHANGED")
		}
		var owner *expiryInvocationOwner
		owner.appConstructed(nil)
		owner.workerConstructed(nil, nil)
		owner.goroutineStarted(nil)
	}); n != 0 {
		t.Fatal("FAPP_DEFAULT_ALLOCATION")
	}
	p := newExpiryInvocationPlan(expiryOwnerAuthFirst)
	if p.capability() != nil || p.view().claimed || p.view().n != 0 {
		t.Fatal("FAPP_PREMINTED_SOURCE")
	}
	seen := 0
	observer := financial.ExpiryObserver(func(financial.ExpiryObservation) { seen++ })
	startup := withExpiryInvocationOwner(withExpiryOpenObserver(ctx, observer), p)
	if beginExpiryInvocationOwner(startup) == nil {
		t.Fatal("FAPP_CARRIER_NO_SOURCE")
	}
	actual, got := takeExpiryOpenObserver(startup)
	if actual != ctx || got == nil || actual.Done() != ctx.Done() || actual.Value(struct{ marker bool }{true}) != 7 || seen != 0 {
		t.Fatal("FAPP_CARRIER_CONTEXT_CHANGED")
	}
	got(financial.ExpiryObservation{})
	cause := errors.New("synthetic cancellation")
	cancel(cause)
	if seen != 1 || actual.Err() != context.Canceled || context.Cause(actual) != cause {
		t.Fatal("FAPP_CARRIER_CANCELLATION_CHANGED")
	}
}

func TestExpiryInvocationOwner20261022Selection(t *testing.T) {
	for _, scenario := range []string{"begin-refusal", "begin-cancelled", "nil-result", "rows-error", "zero-rows", "multiple-rows", "employee-predicate", "verifier-predicate", "replacement-employee", "replacement-verifier", "pre-commit-refusal", "post-commit-report", ""} {
		if firstOwnerBoundarySelection(scenario) != expiryOwnerUnselected {
			t.Fatal("FAPP_FOREIGN_LEAF_SELECTED")
		}
	}
	if firstOwnerBoundarySelection("confirmation-query-error") != expiryOwnerConfirmFirst {
		t.Fatal("FAPP_CONFIRM_LEAF_NOT_SELECTED")
	}
	ctx := context.Background()
	var unselected slog21Fixture
	if got, p := unselected.firstOwnerStartup(ctx); got != ctx || p != nil || unselected.firstOwnerAttempted {
		t.Fatal("FAPP_DEFAULT_FIXTURE_SELECTED")
	}
	f := &slog21Fixture{firstOwner: newExpiryInvocationPlan(expiryOwnerConfirmFirst)}
	first, p := f.firstOwnerStartup(ctx)
	if first == ctx || p != f.firstOwner || !f.firstOwnerAttempted || p.capability() != nil {
		t.Fatal("FAPP_FIRST_SELECTION")
	}
	owner := beginExpiryInvocationOwner(first)
	before := p.view()
	if second, next := f.firstOwnerStartup(ctx); second != ctx || next != nil || p.view() != before || owner == nil {
		t.Fatal("FAPP_SECOND_OPEN_SELECTED")
	}
}
