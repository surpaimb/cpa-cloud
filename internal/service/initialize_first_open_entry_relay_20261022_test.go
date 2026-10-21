package service

// Independently authored B1 oracles. Direct helpers below are synthetic; only
// the two original-constructor subtests exercise the real caller/entry chain.
import (
	"context"
	"crypto/sha1"
	"database/sql/driver"
	"errors"
	"fmt"
	"go/format"
	"os"
	"regexp"
	"strings"
	"testing"
)

func b1Source(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal("B1_SOURCE_READ")
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

func b1Sources(t *testing.T) map[string]string {
	t.Helper()
	s := make(map[string]string)
	for _, name := range []string{
		"app.go", "config.go", "subscription_expiry_worker.go",
		"self_logout_confirmation_20261021_test.go", "self_logout_prequery_cut_installation_20261021_test.go",
		"expiry_invocation_owner_20261022.go", "expiry_invocation_owner_20261022_test.go",
		"initialize_driver_origin_caller_20261022.go", "initialize_driver_origin_caller_20261022_test.go",
		"initialize_first_open_entry_relay_20261022.go",
	} {
		s[name] = b1Source(t, name)
	}
	return s
}

func b1Blob(src string) string {
	b := []byte(fmt.Sprintf("blob %d\x00", len(src)) + src)
	return fmt.Sprintf("%x", sha1.Sum(b))
}

func b1ProjectedBlob(src string) string {
	b, err := format.Source([]byte(src))
	if err != nil {
		return "parse-error"
	}
	return b1Blob(string(b))
}

func b1DeleteField(src, name, typ string) (string, bool) {
	r := regexp.MustCompile("(?m)^[\\t ]*" + regexp.QuoteMeta(name) + "[\\t ]+" + regexp.QuoteMeta(typ) + "\\n")
	if len(r.FindAllString(src, -1)) != 1 {
		return src, false
	}
	return r.ReplaceAllString(src, ""), true
}

func b1Audit(s map[string]string) string {
	s, admissionLabel := projectConstructorAdmissionB1Inputs(s)
	if admissionLabel != "" {
		return admissionLabel
	}
	owner := s["expiry_invocation_owner_20261022.go"]
	hook := "\tp.firstOpenRelay.receiveAtEntry(p, owner)\n"
	if strings.Count(owner, hook) != 1 || !strings.Contains(owner, "\tp.owner = owner\n\tp.appendLocked(expiryOwnerEntry)\n"+hook+"\treturn owner\n") {
		return "B1_SOURCE_OPEN_ENTRY_MISSING"
	}
	oldOwner := strings.Replace(owner, hook, "", 1)
	var ok bool
	oldOwner, ok = b1DeleteField(oldOwner, "firstOpenRelay", "*initializeFirstOpenRelay")
	if !ok || b1ProjectedBlob(oldOwner) != "13de95f8276eebc89bda79bffead527280d48b40" {
		return "B1_OLD_OWNER_BOUNDARY"
	}
	origin := s["initialize_driver_origin_caller_20261022.go"]
	newComment := "// A separate B1 relay may deliver only to first Open entry; never to\n// an App store, opener, Conn or transaction.\n"
	oldComment := "// No implementation reference is routed to App/Open, a Conn or a transaction.\n"
	newReturn := "\tdelivery := &initializeCallerDelivery{attempt: a, selection: p.selection, implementation: p.implementation}\n\tp.firstOpenDelivery = delivery\n\treturn delivery\n"
	oldReturn := "\treturn &initializeCallerDelivery{attempt: a, selection: p.selection, implementation: p.implementation}\n"
	if strings.Count(origin, newComment) != 1 || strings.Count(origin, newReturn) != 1 || !strings.Contains(origin, "\tp.delivered = true\n"+newReturn) {
		return "B1_CANONICAL_SOURCE"
	}
	oldOrigin := strings.Replace(strings.Replace(origin, newComment, oldComment, 1), newReturn, oldReturn, 1)
	for _, field := range [][2]string{{"firstOpenDelivery", "*initializeCallerDelivery"}, {"firstOpenRelay", "*initializeFirstOpenRelay"}} {
		oldOrigin, ok = b1DeleteField(oldOrigin, field[0], field[1])
		if !ok {
			return "B1_ORIGIN_FIELD"
		}
	}
	if b1ProjectedBlob(oldOrigin) != "1c60fccf7269c89040c363ebea7d334249db9aaa" {
		return "B1_OLD_ORIGIN_BOUNDARY"
	}
	initTest := s["initialize_driver_origin_caller_20261022_test.go"]
	prepare := "\tprepareFirstOpenRelay(f.initializeOrigin, f.firstOwner)\n"
	arm := "\tarmFirstOpenRelay(f.initializeOrigin, f.initializeDelivery)\n"
	report := "\tf.installFirstOpenRelayReport(t)\n"
	if !strings.Contains(initTest, "func (f *slog21Fixture) initializeOriginStartup(ctx context.Context) context.Context {\n"+prepare+"\treturn withInitializeOrigin(ctx, f.initializeOrigin)") ||
		!strings.Contains(initTest, "\tf.initializeDelivery = f.initializeOrigin.callerReturned(f.initializeOrigin.attemptToken(), actualErr)\n"+arm) ||
		!strings.Contains(initTest, "\tf.initializeOrigin = newInitializeOriginPlan(scope)\n"+report+"\tt.Cleanup(") {
		return "B1_ACTUAL_CALLER_SEAMS"
	}
	for _, add := range []string{prepare, arm, report} {
		if strings.Count(initTest, add) != 1 {
			return "B1_CALLER_MULTIPLICITY"
		}
		initTest = strings.Replace(initTest, add, "", 1)
	}
	if b1ProjectedBlob(initTest) != "1ec1ea179f78553066d92fdf309924f33e5edfc5" {
		return "B1_OLD_INIT_TEST_BOUNDARY"
	}
	for name, blob := range map[string]string{
		"app.go":                        "22444c3687485b71f58d9b367697c99acb9e7892",
		"config.go":                     "0371095daeb227bce03ac96d3f3ba3eef75225f9",
		"subscription_expiry_worker.go": "5badd89c6e9436f0c13f3a84fb4d51354d40ff41",
		"self_logout_confirmation_20261021_test.go":              "f6240407202ea487efdc68cb6034c7d970627619",
		"expiry_invocation_owner_20261022_test.go":               "3f309a171577922d18e8193f093326365fbc4e05",
		"self_logout_prequery_cut_installation_20261021_test.go": "28f25e79e30f3c390fb1acc35c972f037b18e527",
	} {
		if b1Blob(s[name]) != blob {
			return "B1_FROZEN_SOURCE_BOUNDARY"
		}
	}
	return ""
}

func b1Changed(s map[string]string, name, from, to string) map[string]string {
	copy := make(map[string]string, len(s))
	for k, v := range s {
		copy[k] = v
	}
	copy[name] = strings.Replace(copy[name], from, to, 1)
	return copy
}

func TestInitializeFirstOpenEntryRelay20261022Source(t *testing.T) {
	s := b1Sources(t)
	if label := b1Audit(s); label != "" {
		t.Fatal(label)
	}
	for _, m := range []struct{ name, file, from, to string }{
		{"delete-entry-consumption", "expiry_invocation_owner_20261022.go", "\tp.firstOpenRelay.receiveAtEntry(p, owner)\n", ""},
		{"consume-after-helper-return", "expiry_invocation_owner_20261022.go", "\tp.firstOpenRelay.receiveAtEntry(p, owner)\n\treturn owner", "\treturn owner\n\tp.firstOpenRelay.receiveAtEntry(p, owner)"},
		{"move-after-store", "app.go", "\ts, err := openStore(cfg.DataDir)", "\ts, err := openStore(cfg.DataDir)\n\towner.plan.firstOpenRelay.receiveAtEntry(owner.plan, owner)"},
		{"copy-canonical", "initialize_driver_origin_caller_20261022.go", "\tp.firstOpenDelivery = delivery", "\tcopy := *delivery\n\tp.firstOpenDelivery = &copy"},
		{"forged-actual-capability", "expiry_invocation_owner_20261022.go", "p.firstOpenRelay.receiveAtEntry(p, owner)", "p.firstOpenRelay.receiveAtEntry(p, &expiryInvocationOwner{plan: p})"},
		{"learn-target-from-receipt", "initialize_driver_origin_caller_20261022_test.go", "prepareFirstOpenRelay(f.initializeOrigin, f.firstOwner)", "prepareFirstOpenRelay(f.initializeDelivery.attempt.plan, f.firstOwner)"},
		{"arm-forged-nil", "self_logout_confirmation_20261021_test.go", "f.initializeOriginReturned(initializeErr)", "f.initializeOriginReturned(nil)"},
		{"helper-only-no-entry", "expiry_invocation_owner_20261022.go", "\tp.firstOpenRelay.receiveAtEntry(p, owner)\n", "\t// synthetic helper tests alone report success\n"},
		{"late-caller-arm", "initialize_driver_origin_caller_20261022_test.go", "\tarmFirstOpenRelay(f.initializeOrigin, f.initializeDelivery)\n", ""},
		{"change-business-error", "app.go", "return nil, errors.New(\"employee self wallet balance requires employee self service\")", "return nil, nil"},
	} {
		if !t.Run(m.name, func(t *testing.T) {
			mutant := b1Changed(s, m.file, m.from, m.to)
			if mutant[m.file] == s[m.file] || b1Audit(mutant) == "" {
				t.Fatal("B1_SOURCE_MUTATION_ACCEPTED")
			}
		}) {
			return
		}
	}
}

func TestInitializeFirstOpenEntryRelay20261022InheritedGuards(t *testing.T) {
	s := b1Sources(t)
	cfg, fixture, guard := s["config.go"], s["self_logout_confirmation_20261021_test.go"], s["expiry_invocation_owner_20261022_test.go"]
	app, worker := s["app.go"], s["subscription_expiry_worker.go"]
	if idocAudit(cfg, fixture, guard) != "" || eioAudit(app, worker, fixture) != "" {
		t.Fatal("B1_INHERITED_SOURCE_GUARD")
	}
	fs, parsed := spqiParse(t, []byte(fixture))
	if spqiAudit(fs, parsed) != "" {
		t.Fatal("B1_INHERITED_GATE4_GUARD")
	}
	for _, m := range []struct{ name, c, f, g string }{
		{"old-getter-before-defer", strings.Replace(cfg, "\tdefer s.close()\n\tif initOrigin", "\tif initOrigin", 1), fixture, guard},
		{"old-wrong-receiver", strings.Replace(cfg, "s.db.Driver()", "other.db.Driver()", 1), fixture, guard},
		{"old-unconditional-getter", strings.Replace(cfg, "initOrigin != nil && initOrigin.bindOriginalStore(s)", "true", 1), fixture, guard},
		{"old-forged-nil", cfg, strings.Replace(fixture, "f.initializeOriginReturned(initializeErr)", "f.initializeOriginReturned(nil)", 1), guard},
		{"old-early-caller", cfg, strings.Replace(fixture, "\tinitializeErr := Initialize", "\tf.initializeOriginReturned(nil)\n\tinitializeErr := Initialize", 1), guard},
		{"old-lost-prepared", strings.Replace(cfg, "initOrigin.nilResultPrepared()", "initOrigin.view()", 1), fixture, guard},
		{"old-error-changed", strings.Replace(cfg, "return errors.New(\"data directory is required\")", "return nil", 1), fixture, guard},
		{"old-hash-weakened", cfg, fixture, strings.Replace(guard, "82b469869a5ca87decd34e98f4acad477163c10da87cb638e783e66619876944", "changed", 1)},
	} {
		if !t.Run(m.name, func(t *testing.T) {
			if idocAudit(m.c, m.f, m.g) == "" {
				t.Fatal("B1_OLD_INIT_MUTATION_ACCEPTED")
			}
		}) {
			return
		}
	}
	for _, m := range []struct{ name, a, w string }{
		{"old-app-worker", app, strings.Replace(worker, "owner: app.expiryOwner", "owner: nil", 1)},
		{"old-premature-return", strings.Replace(app, "\topened := false", "\towner.plan.callerReturned(owner, app, nil)\n\topened := false", 1), worker},
		{"old-worker-parent", app, strings.Replace(worker, "context.WithCancel(context.Background())", "context.WithCancel(context.TODO())", 1)},
	} {
		if !t.Run(m.name, func(t *testing.T) {
			if eioAudit(m.a, m.w, fixture) == "" {
				t.Fatal("B1_OLD_OWNER_MUTATION_ACCEPTED")
			}
		}) {
			return
		}
	}
}

func TestInitializeFirstOpenEntryRelay20261022StateValues(t *testing.T) {
	if [4]uint8{uint8(firstOpenEntryNone), uint8(firstOpenEntryPrebound), uint8(firstOpenEntryArmed), uint8(firstOpenEntryReceived)} != [4]uint8{0, 1, 2, 3} ||
		[2]uint8{uint8(firstOpenEntryArmEvent), uint8(firstOpenEntryReceiveEvent)} != [2]uint8{1, 2} {
		t.Fatal("B1_STATE_VALUES")
	}
	var r *initializeFirstOpenRelay
	if r.view() != (firstOpenEntryView{}) || r.view().admitted {
		t.Fatal("B1_ZERO_POSITIVE")
	}
}

func TestInitializeFirstOpenEntryRelay20261022DefaultContext(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.WithValue(context.Background(), struct{ b1 bool }{true}, 7))
	defer cancel(nil)
	probe := &idocContextProbe{Context: ctx}
	if n := testing.AllocsPerRun(20, func() {
		prepareFirstOpenRelay(nil, nil)
		armFirstOpenRelay(nil, nil)
		var r *initializeFirstOpenRelay
		r.receiveAtEntry(nil, nil)
		if firstOpenRelayFor(nil) != nil || r.view() != (firstOpenEntryView{}) || withInitializeOrigin(probe, nil) != probe || withExpiryInvocationOwner(probe, nil) != probe || beginExpiryInvocationOwner(probe) != nil {
			panic("B1_DEFAULT_EFFECT")
		}
	}); n != 0 || probe.reads != 0 {
		t.Fatal("B1_DEFAULT_ALLOCATION_OR_CONTEXT_READ")
	}
	p := newExpiryInvocationPlan(expiryOwnerAuthFirst)
	actual, observer := takeExpiryOpenObserver(withExpiryInvocationOwner(probe, p))
	if actual != probe || observer != nil || probe.reads != 0 || firstOpenRelayFor(nil) != nil {
		t.Fatal("B1_CONTEXT_UNWRAP")
	}
	cause := errors.New("synthetic B1 cancellation")
	cancel(cause)
	if actual.Done() != ctx.Done() || actual.Value(struct{ b1 bool }{true}) != 7 || context.Cause(actual) != cause {
		t.Fatal("B1_CONTEXT_SEMANTICS")
	}
}

func (f *slog21Fixture) installFirstOpenRelayReport(t *testing.T) {
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("B1_ENTRY_NOT_ACCEPTED_PRIOR_FAILURE")
			return
		}
		r := firstOpenRelayFor(f.initializeOrigin)
		if r == nil || !f.initializeOrigin.view().admitted || !r.view().admitted {
			t.Error("B1_ENTRY_MISSING")
			return
		}
		owner := f.firstOwner.capability()
		r.mu.Lock()
		matched := r.origin == f.initializeOrigin && r.target == f.firstOwner && r.delivery == f.initializeDelivery && r.owner == owner
		r.mu.Unlock()
		if !matched {
			t.Error("B1_ENTRY_SOURCE_MISMATCH")
			return
		}
		t.Log("OPEN_ENTRY_REFERENCE_ONLY ENTRY_RECEIVED")
	})
}

// Synthetic metadata built from already captured references; no getter, driver,
// DB/Conn, Initialize, Open, process or extra lifetime is used by these controls.
func b1Captured(t *testing.T, s *store, d driver.Driver) (*initializeOriginPlan, *initializeOriginAttempt, *expiryInvocationPlan, *initializeFirstOpenRelay) {
	t.Helper()
	p := newInitializeOriginPlan(initializeOriginAuth)
	target := newExpiryInvocationPlan(expiryOwnerAuthFirst)
	prepareFirstOpenRelay(p, target)
	_, a := takeInitializeOrigin(withInitializeOrigin(context.Background(), p))
	if a == nil || !a.bindOriginalStore(s) {
		t.Fatal("B1_SYNTHETIC_BIND")
	}
	a.captureFromStore(s, d)
	return p, a, target, firstOpenRelayFor(p)
}

func b1Finish(p *initializeOriginPlan, a *initializeOriginAttempt) *initializeCallerDelivery {
	a.nilResultPrepared()
	return p.callerReturned(a, nil)
}

func b1Controls(t *testing.T, s, otherRoot *store, d driver.Driver) {
	t.Helper()
	for _, name := range []string{"failed-init", "pending-init", "foreign-receipt", "copied-receipt", "missing-receipt", "missing-canonical", "foreign-attempt", "wrong-selection", "wrong-origin-plan", "wrong-target-same-driver-root", "wrong-capability", "duplicate-prebind", "duplicate-arm", "late-arm", "unknown-state", "missing-prebind"} {
		if !t.Run(name, func(t *testing.T) {
			p, a, target, r := b1Captured(t, s, d)
			var receipt *initializeCallerDelivery
			switch name {
			case "failed-init":
				a.nilResultPrepared()
				receipt = p.callerReturned(a, errors.New("synthetic failed Init"))
			case "pending-init":
				a.nilResultPrepared()
			default:
				receipt = b1Finish(p, a)
			}
			switch name {
			case "foreign-receipt":
				foreign, attempt, _, _ := b1Captured(t, otherRoot, d)
				receipt = b1Finish(foreign, attempt)
			case "copied-receipt":
				copy := *receipt
				receipt = &copy
			case "missing-receipt":
				receipt = nil
			case "missing-canonical":
				p.firstOpenDelivery = nil
			case "foreign-attempt":
				receipt.attempt = &initializeOriginAttempt{plan: p}
			case "wrong-selection":
				receipt.selection = initializeOriginConfirm
			case "wrong-origin-plan":
				r.origin = newInitializeOriginPlan(initializeOriginAuth)
			case "duplicate-prebind":
				prepareFirstOpenRelay(p, target)
			case "late-arm":
				beginExpiryInvocationOwner(withExpiryInvocationOwner(context.Background(), target))
			case "unknown-state":
				r.phase = firstOpenEntryPhase(255)
			case "missing-prebind":
				p.firstOpenRelay = nil
			}
			oldInit := p.view()
			armFirstOpenRelay(p, receipt)
			if p.view() != oldInit {
				t.Fatal("B1_CHANGED_OLD_INIT_HISTORY")
			}
			switch name {
			case "duplicate-arm":
				armFirstOpenRelay(p, receipt)
			case "wrong-target-same-driver-root":
				_, _, foreignTarget, _ := b1Captured(t, otherRoot, d)
				foreignTarget.firstOpenRelay = r // Unpublished synthetic wrong-root association.
				beginExpiryInvocationOwner(withExpiryInvocationOwner(context.Background(), foreignTarget))
			case "wrong-capability":
				r.receiveAtEntry(target, &expiryInvocationOwner{plan: target})
			}
			beginExpiryInvocationOwner(withExpiryInvocationOwner(context.Background(), target))
			if v := r.view(); v.admitted || v.received || !v.invalid {
				t.Fatal("B1_INVALID_RELAY_ACCEPTED")
			}
		}) {
			return
		}
	}
	if !t.Run("foreign-target-no-overwrite", func(t *testing.T) {
		_, _, target, original := b1Captured(t, s, d)
		before := original.view()
		foreign := newInitializeOriginPlan(initializeOriginAuth)
		prepareFirstOpenRelay(foreign, target)
		rejected := firstOpenRelayFor(foreign)
		if target.firstOpenRelay != original || original.view() != before || rejected == nil || !rejected.view().invalid || rejected.view().targetBound {
			t.Fatal("B1_FOREIGN_TARGET_OVERWRITE")
		}
	}) {
		return
	}
	if !t.Run("duplicate-entry-preserves-history", func(t *testing.T) {
		p, a, target, r := b1Captured(t, s, d)
		armFirstOpenRelay(p, b1Finish(p, a))
		owner := beginExpiryInvocationOwner(withExpiryInvocationOwner(context.Background(), target))
		before := r.view()
		if !before.admitted || !before.received || before.n != 2 {
			t.Fatal("B1_SYNTHETIC_CLASSIFICATION")
		}
		r.receiveAtEntry(target, owner)
		after := r.view()
		if !after.invalid || after.admitted || !after.received || after.n != before.n || after.events != before.events {
			t.Fatal("B1_DUPLICATE_REWROTE_HISTORY")
		}
	}) {
		return
	}
	if !t.Run("open-error-is-not-app-success", func(t *testing.T) {
		p, a, target, r := b1Captured(t, s, d)
		armFirstOpenRelay(p, b1Finish(p, a))
		owner := beginExpiryInvocationOwner(withExpiryInvocationOwner(context.Background(), target))
		before := r.view()
		target.callerReturned(owner, nil, errors.New("synthetic later Open error"))
		if !before.received || r.view() != before || target.view().accepted || target.view().returned != expiryOwnerFailed {
			t.Fatal("B1_OPEN_ERROR_PROMOTED")
		}
		t.Log("B1_SYNTHETIC_CLASSIFICATION_ONLY")
	}) {
		return
	}
	t.Run("capacity-preserves-prefix", func(t *testing.T) {
		_, _, _, r := b1Captured(t, s, d)
		r.n = 2
		r.events = [2]firstOpenEntryEvent{1, 2}
		before := r.events
		if r.appendLocked(firstOpenEntryReceiveEvent) || !r.invalid || r.n != 2 || r.events != before {
			t.Fatal("B1_CAPACITY_OVERWRITE")
		}
	})
}

func b1RealEntry(t *testing.T, f *slog21Fixture, scope initializeOriginSelection) (*store, driver.Driver) {
	t.Helper()
	p := f.initializeOrigin
	r := firstOpenRelayFor(p)
	if p == nil || r == nil || f.initializeDelivery == nil {
		t.Fatal("B1_REAL_CALLER_MISSING")
	}
	owner := f.firstOwner.capability()
	v := r.view()
	r.mu.Lock()
	matched := r.origin == p && r.target == f.firstOwner && r.delivery == f.initializeDelivery && r.owner == owner
	r.mu.Unlock()
	p.mu.Lock()
	canonical := p.firstOpenDelivery == f.initializeDelivery && f.initializeDelivery.attempt == p.attempt && p.attempt != nil && p.attempt.plan == p
	s, d := p.store, p.implementation
	p.mu.Unlock()
	if !matched || !canonical || owner == nil || owner.plan != f.firstOwner || !f.firstOwnerAttempted || !p.view().admitted ||
		!v.admitted || !v.referenceOnly || !v.targetBound || !v.armed || !v.received || v.selection != scope || v.phase != firstOpenEntryReceived ||
		v.n != 2 || v.events != [2]firstOpenEntryEvent{1, 2} || s == nil || s.db == nil || d == nil {
		t.Fatal("B1_REAL_SOURCE_CHAIN")
	}
	ctx := context.Background()
	before := r.view()
	secondStartup, secondPlan := f.firstOwnerStartup(ctx)
	if secondStartup != ctx || secondPlan != nil || beginExpiryInvocationOwner(secondStartup) != nil || r.view() != before {
		t.Fatal("B1_LEGITIMATE_SECOND_STARTUP_SELECTED")
	}
	t.Log("B1_REAL_ORIGINAL_CALLER_FIRST_OPEN_ENTRY_CONFIRMED")
	t.Log("OPEN_ENTRY_REFERENCE_ONLY ENTRY_RECEIVED")
	return s, d
}

func TestInitializeFirstOpenEntryRelay20261022RealSeam(t *testing.T) {
	// The existing original constructor, not a helper-only or mirror Open.
	var first *store
	var implementation driver.Driver
	if !t.Run("auth-original-constructor-entry", func(t *testing.T) {
		f := newSlog21(t, expiryOwnerAuthFirst)
		first, implementation = b1RealEntry(t, f, initializeOriginAuth)
	}) {
		return
	}
	// The original first constructor cleanup completed before the next begins.
	t.Run("confirm-original-constructor-entry", func(t *testing.T) {
		f := newSlog21(t, expiryOwnerConfirmFirst)
		second, sameImplementation := b1RealEntry(t, f, initializeOriginConfirm)
		if first == nil || first == second || first.db == second.db || implementation != sameImplementation {
			t.Fatal("B1_REAL_DISTINCT_ROOT_SINGLETON_CONTROL")
		}
		b1Controls(t, second, first, sameImplementation)
	})
}
