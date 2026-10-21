package service

// Independently authored source oracle for the caller-only Init-origin contract.
// This first control executes no Initialize, App, SQL or driver method.

import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"errors"
	"fmt"
	"go/format"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func idocSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("INIT_ORIGIN_SOURCE_READ")
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// New independent cleanup is registered before the old owner cleanup, and so
// runs after both original reports. No source value/error is formatted.
func (f *slog21Fixture) installInitializeOrigin(t *testing.T, selection []expiryOwnerSelection) {
	if len(selection) == 0 || len(selection) == 1 && selection[0] == expiryOwnerUnselected {
		return
	}
	scope := initializeOriginNone
	if len(selection) == 1 {
		switch selection[0] {
		case expiryOwnerAuthFirst:
			scope = initializeOriginAuth
		case expiryOwnerConfirmFirst:
			scope = initializeOriginConfirm
		}
	}
	f.initializeOrigin = newInitializeOriginPlan(scope)
	f.installFirstOpenRelayReport(t)
	t.Cleanup(func() {
		v := f.initializeOrigin.view()
		if v.admitted && f.initializeDelivery != nil && f.initializeDelivery.attempt == f.initializeOrigin.attemptToken() {
			switch v.selection {
			case initializeOriginAuth:
				t.Log("INIT_ORIGIN_CALLER_DELIVERED_AUTH")
			case initializeOriginConfirm:
				t.Log("INIT_ORIGIN_CALLER_DELIVERED_CONFIRM")
			default:
				t.Error("INIT_ORIGIN_CALLER_SELECTION")
			}
			if t.Failed() {
				t.Log("INIT_ORIGIN_CALLER_CASE_FAILED_NO_CASE_GO")
			}
			return
		}
		if t.Failed() {
			t.Log("INIT_ORIGIN_CALLER_NOT_ACCEPTED_PRIOR_FAILURE")
			return
		}
		t.Error("INIT_ORIGIN_CALLER_MISSING")
	})
}

func (f *slog21Fixture) initializeOriginStartup(ctx context.Context) context.Context {
	prepareFirstOpenRelay(f.initializeOrigin, f.firstOwner)
	return withInitializeOrigin(ctx, f.initializeOrigin)
}

func (f *slog21Fixture) initializeOriginReturned(actualErr error) {
	if f.initializeOrigin == nil {
		return
	}
	if f.initializeDelivery != nil {
		f.initializeOrigin.reject()
		return
	}
	f.initializeDelivery = f.initializeOrigin.callerReturned(f.initializeOrigin.attemptToken(), actualErr)
	armFirstOpenRelay(f.initializeOrigin, f.initializeDelivery)
}

func idocFormatHash(src string) string {
	b, err := format.Source([]byte(src))
	if err != nil {
		return "parse-error"
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// Full prior PR113 fixture projection, before invoking the unchanged older
// owner stripping/hash. Only these exact seams and numeric anchor relocation
// are admissible; refreshing an old hash cannot hide a wider modification.
func projectInitializeOriginCallerFixture(src string) (string, string) {
	setup := "\tf.installInitializeOrigin(t, selection)\n"
	newCall := "\tinitializeErr := Initialize(f.initializeOriginStartup(ctx), f.dir, strings.NewReader(\"synthetic-self-logout-admin\\n\"))\n\tf.initializeOriginReturned(initializeErr)\n\tif initializeErr != nil {\n"
	oldCall := "\tif Initialize(ctx, f.dir, strings.NewReader(\"synthetic-self-logout-admin\\n\")) != nil {\n"
	if strings.Count(src, setup) != 1 || strings.Count(src, newCall) != 1 {
		return "", "INIT_ORIGIN_FIXTURE_SEAM"
	}
	src = strings.Replace(src, setup, "", 1)
	src = strings.Replace(src, newCall, oldCall, 1)
	for _, field := range []string{"initializeOrigin", "initializeDelivery"} {
		r := regexp.MustCompile("(?m)^[\\t ]*" + field + "[\\t ]+[^\\n]+\\n")
		if len(r.FindAllString(src, -1)) != 1 {
			return "", "INIT_ORIGIN_FIXTURE_FIELD"
		}
		src = r.ReplaceAllString(src, "")
	}
	b, err := format.Source([]byte(src))
	if err != nil {
		return "", "INIT_ORIGIN_FIXTURE_PARSE"
	}
	old := string(b)
	normalized := regexp.MustCompile("line: +[0-9]+").ReplaceAllString(old, "line: 0")
	if fmt.Sprintf("%x", sha256.Sum256([]byte(normalized))) != "8f6f12b14f0ff64787784bddf272484c5cb11bbd8a4a597a47a3cec2bcfecb09" {
		return "", "INIT_ORIGIN_FIXTURE_BOUNDARY"
	}
	return old, ""
}

func idocAudit(config, fixture, ownerGuard string) string {
	entry := "\tctx, initOrigin := takeInitializeOrigin(ctx)\n"
	capture := "\tif initOrigin != nil && initOrigin.bindOriginalStore(s) {\n\t\tinitOrigin.captureFromStore(s, s.db.Driver())\n\t}\n"
	prepared := "\tif initOrigin != nil {\n\t\tinitOrigin.nilResultPrepared()\n\t}\n"
	if !strings.Contains(config, "func InitializeWithConfig(ctx context.Context, cfg Config, passwordReader io.Reader) error {\n"+entry) ||
		!strings.Contains(config, "\tdefer s.close()\n"+capture+"\tvar count int") ||
		!strings.Contains(config, prepared+"\treturn nil\n}") || strings.Count(config, "s.db.Driver()") != 1 {
		return "INIT_ORIGIN_SOURCE_MISSING"
	}
	old := config
	for _, add := range []string{entry, capture, prepared} {
		if strings.Count(old, add) != 1 {
			return "INIT_ORIGIN_CONFIG_MULTIPLICITY"
		}
		old = strings.Replace(old, add, "", 1)
	}
	if idocFormatHash(old) != "b6172898796aa43f62469f38c83e6f941af4e4006cd04b40a58ab3c44f9a33cd" {
		return "INIT_ORIGIN_CONFIG_BOUNDARY"
	}
	if _, label := projectInitializeOriginCallerFixture(fixture); label != "" {
		return label
	}
	projectionCall := "\tfixture, originLabel := projectInitializeOriginCallerFixture(fixture)\n\tif originLabel != \"\" {\n\t\treturn originLabel\n\t}\n"
	if strings.Count(ownerGuard, projectionCall) != 1 {
		return "INIT_ORIGIN_OWNER_PROJECTION"
	}
	oldGuard := strings.Replace(ownerGuard, projectionCall, "", 1)
	if idocFormatHash(oldGuard) != "9c55b44f8d5ab6b9af501febfa35c2e555d0edf53c460328348e9d7061416453" {
		return "INIT_ORIGIN_OLD_OWNER_BOUNDARY"
	}
	return ""
}

func TestInitializeDriverOriginCaller20261022Source(t *testing.T) {
	cfg, fixture, oldGuard := idocSource(t, "config.go"), idocSource(t, "self_logout_confirmation_20261021_test.go"), idocSource(t, "expiry_invocation_owner_20261022_test.go")
	if label := idocAudit(cfg, fixture, oldGuard); label != "" {
		t.Fatal(label)
	}
	app, worker := idocSource(t, "app.go"), idocSource(t, "subscription_expiry_worker.go")
	if eioAudit(app, worker, fixture) != "" {
		t.Fatal("INIT_ORIGIN_OLD_OWNER_GUARD")
	}
	fs, parsed := spqiParse(t, []byte(fixture))
	if spqiAudit(fs, parsed) != "" {
		t.Fatal("INIT_ORIGIN_OLD_GATE4_GUARD")
	}
	for _, m := range []struct{ name, cfg, fixture, guard string }{
		{"getter-before-defer", strings.Replace(cfg, "\tdefer s.close()\n\tif initOrigin", "\tif initOrigin", 1), fixture, oldGuard},
		{"wrong-receiver", strings.Replace(cfg, "s.db.Driver()", "other.db.Driver()", 1), fixture, oldGuard},
		{"unconditional-getter", strings.Replace(cfg, "initOrigin != nil && initOrigin.bindOriginalStore(s)", "true", 1), fixture, oldGuard},
		{"forged-nil", cfg, strings.Replace(fixture, "f.initializeOriginReturned(initializeErr)", "f.initializeOriginReturned(nil)", 1), oldGuard},
		{"early-caller", cfg, strings.Replace(fixture, "\tinitializeErr := Initialize", "\tf.initializeOriginReturned(nil)\n\tinitializeErr := Initialize", 1), oldGuard},
		{"lost-callee-prepared", strings.Replace(cfg, "initOrigin.nilResultPrepared()", "initOrigin.view()", 1), fixture, oldGuard},
		{"changed-original-error", strings.Replace(cfg, "return errors.New(\"data directory is required\")", "return nil", 1), fixture, oldGuard},
		{"weakened-owner-hash", cfg, fixture, strings.Replace(oldGuard, "82b469869a5ca87decd34e98f4acad477163c10da87cb638e783e66619876944", "changed", 1)},
	} {
		if !t.Run(m.name, func(t *testing.T) {
			if idocAudit(m.cfg, m.fixture, m.guard) == "" {
				t.Fatal("INIT_ORIGIN_STATIC_MUTATION_ACCEPTED")
			}
		}) {
			return
		}
	}
	for _, m := range []struct{ name, app, worker string }{
		{"old-drop-app-worker", app, strings.Replace(worker, "owner: app.expiryOwner", "owner: nil", 1)},
		{"old-premature-owner-return", strings.Replace(app, "\topened := false", "\towner.plan.callerReturned(owner, app, nil)\n\topened := false", 1), worker},
		{"old-background-parent", app, strings.Replace(worker, "context.WithCancel(context.Background())", "context.WithCancel(context.TODO())", 1)},
	} {
		if !t.Run(m.name, func(t *testing.T) {
			if eioAudit(m.app, m.worker, fixture) == "" {
				t.Fatal("INIT_ORIGIN_OLD_MUTATION_ACCEPTED")
			}
		}) {
			return
		}
	}
}

func TestInitializeDriverOriginCaller20261022StateValues(t *testing.T) {
	if [3]uint8{uint8(initializeOriginNone), uint8(initializeOriginAuth), uint8(initializeOriginConfirm)} != [3]uint8{0, 1, 2} ||
		[8]uint8{uint8(initializeOriginNew), uint8(initializeOriginBound), uint8(initializeOriginEntered), uint8(initializeOriginStore), uint8(initializeOriginCaptured), uint8(initializeOriginPrepared), uint8(initializeOriginReturned), uint8(initializeOriginDelivered)} != [8]uint8{0, 1, 2, 3, 4, 5, 6, 7} ||
		[4]uint8{uint8(initializeOriginPending), uint8(initializeOriginSuccess), uint8(initializeOriginFailed), uint8(initializeOriginUnknown)} != [4]uint8{0, 1, 2, 3} {
		t.Fatal("INIT_ORIGIN_STATE_VALUES")
	}
	var p *initializeOriginPlan
	if p.view() != (initializeOriginView{}) {
		t.Fatal("INIT_ORIGIN_ZERO_VALUE")
	}
}

type idocContextProbe struct {
	context.Context
	reads int
}

func (p *idocContextProbe) Deadline() (time.Time, bool) { p.reads++; return p.Context.Deadline() }
func (p *idocContextProbe) Done() <-chan struct{}       { p.reads++; return p.Context.Done() }
func (p *idocContextProbe) Err() error                  { p.reads++; return p.Context.Err() }
func (p *idocContextProbe) Value(key any) any           { p.reads++; return p.Context.Value(key) }

func TestInitializeDriverOriginCaller20261022DefaultContext(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.WithValue(context.Background(), struct{ purpose int }{1}, 7))
	defer cancel(nil)
	probe := &idocContextProbe{Context: ctx}
	if n := testing.AllocsPerRun(20, func() {
		if withInitializeOrigin(probe, nil) != probe {
			panic("INIT_ORIGIN_DEFAULT_WRAPPER")
		}
		original, a := takeInitializeOrigin(probe)
		if original != probe || a != nil {
			panic("INIT_ORIGIN_DEFAULT_ATTEMPT")
		}
	}); n != 0 || probe.reads != 0 {
		t.Fatal("INIT_ORIGIN_DEFAULT_EFFECT")
	}
	p := newInitializeOriginPlan(initializeOriginAuth)
	if p.attemptToken() != nil || p.view().n != 0 {
		t.Fatal("INIT_ORIGIN_PREMINTED_ATTEMPT")
	}
	original, a := takeInitializeOrigin(withInitializeOrigin(probe, p))
	if original != probe || a == nil || probe.reads != 0 {
		t.Fatal("INIT_ORIGIN_UNWRAP_CHANGED_CONTEXT")
	}
	if original.Done() != ctx.Done() || original.Value(struct{ purpose int }{1}) != 7 {
		t.Fatal("INIT_ORIGIN_CONTEXT_VALUE_CHANGED")
	}
	cause := errors.New("synthetic cancellation")
	cancel(cause)
	if original.Err() != context.Canceled || context.Cause(original) != cause {
		t.Fatal("INIT_ORIGIN_CANCELLATION_CHANGED")
	}
	var f slog21Fixture
	if f.initializeOriginStartup(ctx) != ctx {
		t.Fatal("INIT_ORIGIN_DEFAULT_FIXTURE_WRAPPER")
	}
	f.initializeOriginReturned(cause)
	if f.initializeDelivery != nil {
		t.Fatal("INIT_ORIGIN_DEFAULT_DELIVERY")
	}
}

func TestInitializeDriverOriginCaller20261022EntryRejection(t *testing.T) {
	for _, scope := range []initializeOriginSelection{0, 3, 255} {
		p := newInitializeOriginPlan(scope)
		ctx := context.Background()
		original, a := takeInitializeOrigin(withInitializeOrigin(ctx, p))
		if original != ctx || a != nil || !p.view().invalid || p.view().n != 0 {
			t.Fatal("INIT_ORIGIN_INVALID_SELECTION")
		}
	}
	t.Run("duplicate-entry", func(t *testing.T) {
		p := newInitializeOriginPlan(initializeOriginAuth)
		ctx := context.Background()
		startup := withInitializeOrigin(ctx, p)
		_, first := takeInitializeOrigin(startup)
		before := p.view()
		original, second := takeInitializeOrigin(startup)
		if first == nil || second != nil || original != ctx || !p.view().invalid || p.attemptToken() != first || p.view().events != before.events {
			t.Fatal("INIT_ORIGIN_DUPLICATE_ENTRY")
		}
	})
	if t.Failed() {
		return
	}
	t.Run("foreign-carrier", func(t *testing.T) {
		p, other := newInitializeOriginPlan(initializeOriginAuth), newInitializeOriginPlan(initializeOriginConfirm)
		ctx := context.Background()
		startup := withInitializeOrigin(ctx, p)
		b, ok := withInitializeOrigin(startup, other).(initializeOriginBinding)
		if !ok || b.plan != p || b.Context != ctx || !p.view().invalid || !other.view().invalid || p.attemptToken() != nil || other.attemptToken() != nil {
			t.Fatal("INIT_ORIGIN_CARRIER_OVERWRITE")
		}
	})
}

// A typed-nil classifier control only; this method must never be invoked.
type idocNilDriver struct{}

func (*idocNilDriver) Open(string) (driver.Conn, error) { panic("INIT_ORIGIN_UNEXPECTED_DRIVER_OPEN") }

func idocEntered(t *testing.T) (*initializeOriginPlan, *initializeOriginAttempt) {
	t.Helper()
	p := newInitializeOriginPlan(initializeOriginAuth)
	ctx := context.Background()
	original, a := takeInitializeOrigin(withInitializeOrigin(ctx, p))
	if original != ctx || a == nil {
		t.Fatal("INIT_ORIGIN_SYNTHETIC_ENTRY")
	}
	return p, a
}

// Synthetic publication controls reuse references already obtained by the two
// real constructors. They create no DB/Conn, call no Driver getter/method and
// do not claim that this manually advanced plan is an actual Initialize.
func idocCaptured(t *testing.T, s *store, d driver.Driver) (*initializeOriginPlan, *initializeOriginAttempt) {
	t.Helper()
	p, a := idocEntered(t)
	if !a.bindOriginalStore(s) {
		t.Fatal("INIT_ORIGIN_SYNTHETIC_STORE")
	}
	a.captureFromStore(s, d)
	if !p.view().captured || p.view().invalid {
		t.Fatal("INIT_ORIGIN_SYNTHETIC_CAPTURE")
	}
	return p, a
}

func idocControls(t *testing.T, s, foreign *store, d driver.Driver) {
	t.Helper()
	if !t.Run("immutable-delivery", func(t *testing.T) {
		p, a := idocCaptured(t, s, d)
		a.nilResultPrepared()
		before := p.view()
		receipt := p.callerReturned(a, nil)
		after := p.view()
		if receipt == nil || receipt.attempt != a || receipt.implementation != d || !after.admitted || after.n != 7 || after.outcome != initializeOriginSuccess || before.outcome != initializeOriginPending || before.callerSeen {
			t.Fatal("INIT_ORIGIN_DELIVERY_ORDER")
		}
		for i := uint8(0); i < before.n; i++ {
			if after.events[i] != before.events[i] || before.events[i].outcome != initializeOriginPending {
				t.Fatal("INIT_ORIGIN_RETROACTIVE_REWRITE")
			}
		}
		if p.view() != after {
			t.Fatal("INIT_ORIGIN_VIEW_CONSUMED")
		}
	}) {
		return
	}
	for _, scenario := range []string{"failed-after-capture", "failed-after-prepared", "pending-prepared", "forged-nil-without-prepared", "missing-capability", "foreign-capability", "duplicate-store", "duplicate-capture", "late-capture", "second-consumption", "holder-no-overwrite"} {
		if !t.Run(scenario, func(t *testing.T) {
			p, a := idocCaptured(t, s, d)
			switch scenario {
			case "failed-after-capture", "failed-after-prepared":
				if scenario == "failed-after-prepared" {
					a.nilResultPrepared()
				}
				err := errors.New("synthetic Initialize return classification")
				originalErr := err
				if p.callerReturned(a, err) != nil || err != originalErr || p.view().outcome != initializeOriginFailed {
					t.Fatal("INIT_ORIGIN_FAILED_PROMOTED")
				}
			case "pending-prepared":
				a.nilResultPrepared()
				v := p.view()
				if v.invalid || v.delivered || v.admitted || v.callerSeen || v.outcome != initializeOriginPending {
					t.Fatal("INIT_ORIGIN_PREPARED_IS_RETURN")
				}
				return
			case "forged-nil-without-prepared":
				if p.callerReturned(a, nil) != nil || p.view().outcome != initializeOriginUnknown {
					t.Fatal("INIT_ORIGIN_UNKNOWN_PROMOTED")
				}
			case "missing-capability":
				p.callerReturned(nil, nil)
			case "foreign-capability":
				_, other := idocCaptured(t, foreign, d)
				p.callerReturned(other, nil)
			case "duplicate-store":
				if a.bindOriginalStore(s) {
					t.Fatal("INIT_ORIGIN_DUPLICATE_STORE")
				}
			case "duplicate-capture":
				a.captureFromStore(s, d)
			case "late-capture", "second-consumption":
				a.nilResultPrepared()
				if p.callerReturned(a, nil) == nil {
					t.Fatal("INIT_ORIGIN_INITIAL_DELIVERY")
				}
				if scenario == "late-capture" {
					a.captureFromStore(s, d)
				} else if p.callerReturned(a, nil) != nil {
					t.Fatal("INIT_ORIGIN_SECOND_DELIVERY")
				}
			case "holder-no-overwrite":
				a.nilResultPrepared()
				f := &slog21Fixture{initializeOrigin: p}
				f.initializeOriginReturned(nil)
				first := f.initializeDelivery
				f.initializeOriginReturned(nil)
				if first == nil || f.initializeDelivery != first {
					t.Fatal("INIT_ORIGIN_HOLDER_OVERWRITE")
				}
			}
			before := p.view()
			a.nilResultPrepared()
			a.captureFromStore(s, d)
			if p.callerReturned(a, nil) != nil || !before.invalid || p.view().admitted || p.view() != before {
				t.Fatal("INIT_ORIGIN_REJECTION_NOT_LATCHED")
			}
		}) {
			return
		}
	}
	for _, scenario := range []string{"nil-store", "missing-db", "capture-before-bind", "wrong-store-same-singleton", "wrong-db", "nil-driver", "typed-nil-driver", "foreign-producer", "unknown-phase", "capacity"} {
		if !t.Run(scenario, func(t *testing.T) {
			p, a := idocEntered(t)
			switch scenario {
			case "nil-store":
				a.bindOriginalStore(nil)
			case "missing-db":
				a.bindOriginalStore(&store{}) // Inert wrapper only; no DB is created.
			case "capture-before-bind":
				a.captureFromStore(s, d)
			case "wrong-store-same-singleton":
				if !a.bindOriginalStore(s) {
					t.Fatal("INIT_ORIGIN_BIND_CONTROL")
				}
				a.captureFromStore(foreign, d)
			case "wrong-db":
				if !a.bindOriginalStore(s) {
					t.Fatal("INIT_ORIGIN_BIND_CONTROL")
				}
				p.db = foreign.db // Synthetic metadata corruption, not a DB mutation.
				a.captureFromStore(s, d)
			case "nil-driver", "typed-nil-driver":
				if !a.bindOriginalStore(s) {
					t.Fatal("INIT_ORIGIN_BIND_CONTROL")
				}
				var candidate driver.Driver
				if scenario == "typed-nil-driver" {
					candidate = (*idocNilDriver)(nil)
				}
				a.captureFromStore(s, candidate)
			case "foreign-producer":
				forged := &initializeOriginAttempt{plan: p}
				forged.bindOriginalStore(s)
			case "unknown-phase":
				p.phase = initializeOriginPhase(255)
				a.bindOriginalStore(s)
			case "capacity":
				p.n = 7
				previous := p.events
				p.appendLocked() // Unpublished synthetic instance only.
				if p.n != 7 || p.events != previous {
					t.Fatal("INIT_ORIGIN_CAPACITY_OVERWRITE")
				}
			}
			if !p.view().invalid || p.view().delivered || p.callerReturned(a, nil) != nil {
				t.Fatal("INIT_ORIGIN_INVALID_ACCEPTED")
			}
		}) {
			return
		}
	}
}

func idocRealReceipt(t *testing.T, f *slog21Fixture, scope initializeOriginSelection) (*store, driver.Driver) {
	t.Helper()
	p, receipt := f.initializeOrigin, f.initializeDelivery
	if p == nil || receipt == nil {
		t.Fatal("INIT_ORIGIN_REAL_CALLER_MISSING")
	}
	v := p.view()
	if !v.admitted || !v.prepared || !v.callerSeen || !v.attempted || !v.storeBound || !v.captured || v.n != 7 || v.selection != scope || receipt.selection != scope || receipt.attempt != p.attemptToken() || receipt.implementation == nil || receipt.implementation != p.implementation || p.store == nil || p.store.db != p.db || p.store == f.app.store {
		t.Fatal("INIT_ORIGIN_REAL_CALLER_SOURCE")
	}
	if v.events[4] != (initializeOriginEvent{initializeOriginPrepared, initializeOriginPending}) || v.events[5] != (initializeOriginEvent{initializeOriginReturned, initializeOriginSuccess}) || v.events[6] != (initializeOriginEvent{initializeOriginDelivered, initializeOriginSuccess}) {
		t.Fatal("INIT_ORIGIN_REAL_RETURN_EVENTS")
	}
	t.Log("INIT_ORIGIN_REAL_INITIALIZE_TO_CALLER_CONFIRMED")
	return p.store, receipt.implementation
}

func TestInitializeDriverOriginCaller20261022RealSeam(t *testing.T) {
	// New directed constructor tests only: do not invoke any old TestSelfLogout
	// case body, raw/reopen/clean/trigger operation or additional acquisition DB.
	var firstStore *store
	var singleton driver.Driver
	if !t.Run("auth-original-constructor", func(t *testing.T) {
		f := newSlog21(t, expiryOwnerAuthFirst)
		firstStore, singleton = idocRealReceipt(t, f, initializeOriginAuth)
	}) {
		return
	}
	// First constructor's original cleanup has run before the second begins.
	t.Run("confirm-original-constructor", func(t *testing.T) {
		f := newSlog21(t, expiryOwnerConfirmFirst)
		secondStore, sameDriver := idocRealReceipt(t, f, initializeOriginConfirm)
		if firstStore == nil || firstStore == secondStore || firstStore.db == secondStore.db || singleton != sameDriver {
			t.Fatal("INIT_ORIGIN_SINGLETON_NOT_DB_IDENTITY")
		}
		t.Log("INIT_ORIGIN_SAME_IMPLEMENTATION_DISTINCT_INITIALIZE_STORES")
		idocControls(t, secondStore, firstStore, sameDriver)
	})
}
