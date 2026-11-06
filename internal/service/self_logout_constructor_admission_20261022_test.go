package service

// Independently authored test-only admission. The original tests-first oracle
// remains below; its expected missing-source red is retained in the contract.
import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestSelfLogoutConstructorAdmission20261022Source(t *testing.T) {
	b, err := os.ReadFile("self_logout_confirmation_20261021_test.go")
	if err != nil {
		t.Fatal("B2_ADMISSION_SOURCE_READ")
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	if strings.Count(s, "f := newSlog21Admitted(t, admissionNormalSelection(scenario))") != 1 || strings.Count(s, "f := newSlog21Admitted(t, admissionBoundarySelection(scenario), firstOwnerBoundarySelection(scenario))") != 1 {
		t.Fatal("B2_ADMISSION_SOURCE_MISSING")
	}
	sources := b1Sources(t)
	self := b1Source(t, "self_logout_constructor_admission_20261022_test.go")
	b1Test := b1Source(t, "initialize_first_open_entry_relay_20261022_test.go")
	if label := constructorAdmissionAudit(sources, self, b1Test); label != "" {
		t.Fatal(label)
	}
	// Gate4 sees the actual fixture, never a projected set of numeric anchors.
	fs, fixture := spqiParse(t, []byte(s))
	if spqiAudit(fs, fixture) != "" {
		t.Fatal("B2_ADMISSION_ACTUAL_GATE4")
	}
	t.Log("B2_ADMISSION_SOURCE_AND_ACTUAL_GATE4_CONFIRMED")
}

type constructorAdmissionFamily uint8
type constructorAdmissionSelection uint8
type constructorAdmissionPhase uint8

const (
	admissionFamilyNone constructorAdmissionFamily = iota
	admissionFamilyNormal
	admissionFamilyBoundaries
)

const (
	admissionUnselected constructorAdmissionSelection = iota
	admissionNormalIgnored
	admissionBoundaryZero
)

const (
	admissionPhaseUnknown constructorAdmissionPhase = iota
	admissionPhaseReady
	admissionPhaseRecorded
	admissionPhaseClosed
)

type constructorAdmissionFact struct {
	family    constructorAdmissionFamily
	selection constructorAdmissionSelection
}

// Non-zero-size canonical identity: a struct copy does not copy authority.
type constructorAdmissionToken struct{ plan *constructorAdmissionPlan }

type constructorAdmissionChoice struct {
	family    constructorAdmissionFamily
	selection constructorAdmissionSelection
	plan      *constructorAdmissionPlan
	token     *constructorAdmissionToken
}

type constructorAdmissionPlan struct {
	mu        sync.Mutex
	family    constructorAdmissionFamily
	selection constructorAdmissionSelection
	token     *constructorAdmissionToken
	phase     constructorAdmissionPhase
	invalid   bool
	n         uint8
	facts     [1]constructorAdmissionFact
}

type constructorAdmissionView struct {
	family    constructorAdmissionFamily
	selection constructorAdmissionSelection
	phase     constructorAdmissionPhase
	invalid   bool
	admitted  bool
	n         uint8
	facts     [1]constructorAdmissionFact
}

func validConstructorAdmission(f constructorAdmissionFamily, s constructorAdmissionSelection) bool {
	return f == admissionFamilyNormal && s == admissionNormalIgnored || f == admissionFamilyBoundaries && s == admissionBoundaryZero
}

func newConstructorAdmission(f constructorAdmissionFamily, s constructorAdmissionSelection) constructorAdmissionChoice {
	p := &constructorAdmissionPlan{family: f, selection: s, phase: admissionPhaseReady, invalid: !validConstructorAdmission(f, s)}
	p.token = &constructorAdmissionToken{plan: p}
	return constructorAdmissionChoice{family: f, selection: s, plan: p, token: p.token}
}

func admissionNormalSelection(scenario string) constructorAdmissionChoice {
	if scenario != "ignored-body-query" {
		return constructorAdmissionChoice{}
	}
	return newConstructorAdmission(admissionFamilyNormal, admissionNormalIgnored)
}

func admissionBoundarySelection(scenario string) constructorAdmissionChoice {
	if scenario != "zero-rows" {
		return constructorAdmissionChoice{}
	}
	return newConstructorAdmission(admissionFamilyBoundaries, admissionBoundaryZero)
}

func (p *constructorAdmissionPlan) admit(c constructorAdmissionChoice) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.invalid {
		return false
	}
	if c.plan != p || c.token == nil || c.token != p.token || c.token.plan != p || c.family != p.family || c.selection != p.selection || !validConstructorAdmission(p.family, p.selection) || p.phase != admissionPhaseReady || p.n != 0 {
		p.invalid = true
		return false
	}
	p.facts[0] = constructorAdmissionFact{family: p.family, selection: p.selection}
	p.n = 1
	p.phase = admissionPhaseRecorded
	return true
}

// Closing this metadata window is BEFORE the call, not constructor entry.
func (p *constructorAdmissionPlan) closeWindow() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.invalid {
		return
	}
	if p.phase != admissionPhaseRecorded || p.n != 1 {
		p.invalid = true
		return
	}
	p.phase = admissionPhaseClosed
}

func (p *constructorAdmissionPlan) view() constructorAdmissionView {
	if p == nil {
		return constructorAdmissionView{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	accepted := !p.invalid && p.phase == admissionPhaseClosed && p.n == 1 && p.facts[0] == (constructorAdmissionFact{family: p.family, selection: p.selection}) && validConstructorAdmission(p.family, p.selection)
	return constructorAdmissionView{family: p.family, selection: p.selection, phase: p.phase, invalid: p.invalid, admitted: accepted, n: p.n, facts: p.facts}
}

func prepareConstructorAdmission(t *testing.T, choice constructorAdmissionChoice) {
	if choice == (constructorAdmissionChoice{}) {
		return
	}
	t.Helper()
	choice.plan.admit(choice)
	choice.plan.closeWindow()
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("B2_ADMISSION_PRIOR_FAILURE_NO_GO")
			return
		}
		v := choice.plan.view()
		if !v.admitted {
			t.Error("B2_ADMISSION_NOT_ACCEPTED")
			return
		}
		switch v.selection {
		case admissionNormalIgnored:
			t.Log("PRE_CONSTRUCTOR_ADMISSION_ONLY NORMAL_IGNORED")
		case admissionBoundaryZero:
			t.Log("PRE_CONSTRUCTOR_ADMISSION_ONLY BOUNDARIES_ZERO")
		default:
			t.Error("B2_ADMISSION_SELECTION")
		}
	})
}

func newSlog21Admitted(t *testing.T, choice constructorAdmissionChoice, selection ...expiryOwnerSelection) *slog21Fixture {
	t.Helper()
	prepareConstructorAdmission(t, choice)
	return newSlog21(t, selection...)
}

const admissionNormalCall = "\t\t\tf := newSlog21Admitted(t, admissionNormalSelection(scenario))\n"
const admissionBoundaryCall = "\t\t\tf := newSlog21Admitted(t, admissionBoundarySelection(scenario), firstOwnerBoundarySelection(scenario))\n"
const admissionInitShim = "\tsrc, admissionLabel := projectConstructorAdmissionFixture(src)\n\tif admissionLabel != \"\" {\n\t\treturn \"\", admissionLabel\n\t}\n"
const admissionB1Shim = "\ts, admissionLabel := projectConstructorAdmissionB1Inputs(s)\n\tif admissionLabel != \"\" {\n\t\treturn admissionLabel\n\t}\n"

// This integration adds exactly one Handler route to the pinned old App blob.
// Older source oracles keep every original hash; only exact new shims are removed.
const pr136ClientConfigurationRoute = "\tmux.HandleFunc(\"GET /v1/client-configuration\", a.clientConfiguration)\n"
const pr136OwnerShim = "\tapp, routeLabel := pr136ProjectApp(app)\n\tif routeLabel != \"\" {\n\t\treturn routeLabel\n\t}\n"
const pr136OriginShim = "\townerGuard, routeLabel := pr136RequireGuard(ownerGuard, \"owner\")\n\tif routeLabel != \"\" {\n\t\treturn routeLabel\n\t}\n"
const pr136B1Shim = "\ts, routeLabel := pr136ProjectInputs(s)\n\tif routeLabel != \"\" {\n\t\treturn routeLabel\n\t}\n"

func pr136ProjectApp(app string) (string, string) {
	anchor := "\tmux.HandleFunc(\"GET /v1/models\", a.listModels)\n" + pr136ClientConfigurationRoute + "\tmux.HandleFunc(\"POST /v1/chat/completions\", a.chatCompletions)\n"
	if strings.Count(app, pr136ClientConfigurationRoute) != 1 || strings.Count(app, "/v1/client-configuration") != 1 || strings.Count(app, anchor) != 1 {
		return "", "CI_GUARD_PR136_ROUTE_SEAM"
	}
	old := strings.Replace(app, pr136ClientConfigurationRoute, "", 1)
	if b1Blob(old) != "22444c3687485b71f58d9b367697c99acb9e7892" {
		return "", "CI_GUARD_PR136_APP_BOUNDARY"
	}
	return old, ""
}

func pr136GuardSpec(kind string) (string, string, string) {
	switch kind {
	case "owner":
		return "func eioAudit(app, worker, fixture string) string {\n", pr136OwnerShim, "3f309a171577922d18e8193f093326365fbc4e05"
	case "origin":
		return "func idocAudit(config, fixture, ownerGuard string) string {\n", pr136OriginShim, "10909de2a76080171f8053f9482ee3e1f2e75b1c"
	case "b1":
		return "func b1Audit(s map[string]string) string {\n", pr136B1Shim, "638dc621a91a5214346be3c517821cf2413226ee"
	default:
		return "", "", ""
	}
}

func pr136ProjectGuard(src, kind string) (string, string) {
	seam, shim, blob := pr136GuardSpec(kind)
	if blob == "" {
		return "", "CI_GUARD_PR136_GUARD_KIND"
	}
	// Nested historical audits may receive the already projected exact old blob.
	// Actual current-source entry points separately require the one new shim.
	if b1Blob(src) == blob {
		return src, ""
	}
	if strings.Count(src, shim) != 1 || !strings.Contains(src, seam+shim) {
		return "", "CI_GUARD_PR136_GUARD_SEAM"
	}
	old := strings.Replace(src, shim, "", 1)
	if b1Blob(old) != blob {
		return "", "CI_GUARD_PR136_GUARD_BOUNDARY"
	}
	return old, ""
}

func pr136RequireGuard(src, kind string) (string, string) {
	seam, shim, blob := pr136GuardSpec(kind)
	if blob == "" || strings.Count(src, shim) != 1 || !strings.Contains(src, seam+shim) {
		return "", "CI_GUARD_PR136_CURRENT_GUARD_SEAM"
	}
	return pr136ProjectGuard(src, kind)
}

func pr136ProjectInputs(s map[string]string) (map[string]string, string) {
	app, label := pr136ProjectApp(s["app.go"])
	if label != "" {
		return nil, label
	}
	copy := make(map[string]string, len(s))
	for name, src := range s {
		copy[name] = src
	}
	copy["app.go"] = app
	for _, spec := range [][2]string{{"expiry_invocation_owner_20261022_test.go", "owner"}, {"initialize_driver_origin_caller_20261022_test.go", "origin"}} {
		old, label := pr136RequireGuard(s[spec[0]], spec[1])
		if label != "" {
			return nil, label
		}
		copy[spec[0]] = old
	}
	return copy, ""
}

// Nested old audits need baseline acceptance, but the NEW top oracle separately
// requires both actual wiring sites. Reverting both cannot make B2 green.
func projectConstructorAdmissionFixture(src string) (string, string) {
	if b1Blob(src) == "f6240407202ea487efdc68cb6034c7d970627619" {
		return src, ""
	}
	if strings.Count(src, admissionNormalCall) != 1 || strings.Count(src, admissionBoundaryCall) != 1 || strings.Count(src, "newSlog21Admitted(") != 2 {
		return "", "B2_ADMISSION_FIXTURE_SEAM"
	}
	old := strings.Replace(src, admissionNormalCall, "\t\t\tf := newSlog21(t)\n", 1)
	old = strings.Replace(old, admissionBoundaryCall, "\t\t\tf := newSlog21(t, firstOwnerBoundarySelection(scenario))\n", 1)
	if b1Blob(old) != "f6240407202ea487efdc68cb6034c7d970627619" {
		return "", "B2_ADMISSION_FIXTURE_BOUNDARY"
	}
	return old, ""
}

func admissionStripShim(src, seam, shim, blob string) (string, string) {
	if b1Blob(src) == blob {
		return src, ""
	}
	if strings.Count(src, shim) != 1 || !strings.Contains(src, seam+shim) {
		return "", "B2_ADMISSION_GUARD_SEAM"
	}
	old := strings.Replace(src, shim, "", 1)
	if b1Blob(old) != blob {
		return "", "B2_ADMISSION_GUARD_BOUNDARY"
	}
	return old, ""
}

func projectConstructorAdmissionB1Inputs(s map[string]string) (map[string]string, string) {
	fixture, label := projectConstructorAdmissionFixture(s["self_logout_confirmation_20261021_test.go"])
	if label != "" {
		return nil, label
	}
	initSource, label := pr136ProjectGuard(s["initialize_driver_origin_caller_20261022_test.go"], "origin")
	if label != "" {
		return nil, label
	}
	initTest, label := admissionStripShim(initSource, "func projectInitializeOriginCallerFixture(src string) (string, string) {\n", admissionInitShim, "84d470203f9f5cbcadef2d96bc49b49ec9efbd17")
	if label != "" {
		return nil, label
	}
	copy := make(map[string]string, len(s))
	for k, v := range s {
		copy[k] = v
	}
	copy["self_logout_confirmation_20261021_test.go"] = fixture
	copy["initialize_driver_origin_caller_20261022_test.go"] = initTest
	return copy, ""
}

func admissionFunction(src, name string) string {
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "admission.go", src, 0)
	if err != nil {
		return "parse-error"
	}
	var found string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name {
			continue
		}
		if found != "" {
			return "duplicate-function"
		}
		found = src[fs.Position(fn.Pos()).Offset:fs.Position(fn.End()).Offset]
	}
	if found == "" {
		return "missing-function"
	}
	return admissionExpectedFunction(found)
}

func admissionExpectedFunction(src string) string {
	b, err := format.Source([]byte("package service\n" + src))
	if err != nil {
		return "invalid-oracle"
	}
	return string(b)
}

func constructorAdmissionAudit(s map[string]string, self, b1Test string) string {
	fixture := s["self_logout_confirmation_20261021_test.go"]
	if strings.Count(fixture, admissionNormalCall) != 1 || strings.Count(fixture, admissionBoundaryCall) != 1 {
		return "B2_ADMISSION_SOURCE_MISSING"
	}
	if _, label := projectConstructorAdmissionFixture(fixture); label != "" {
		return label
	}
	initTest, routeLabel := pr136RequireGuard(s["initialize_driver_origin_caller_20261022_test.go"], "origin")
	if routeLabel != "" {
		return routeLabel
	}
	b1Test, routeLabel = pr136RequireGuard(b1Test, "b1")
	if routeLabel != "" {
		return routeLabel
	}
	if strings.Count(initTest, admissionInitShim) != 1 || strings.Count(b1Test, admissionB1Shim) != 1 {
		return "B2_ADMISSION_PROJECTION_MISSING"
	}
	if _, label := admissionStripShim(initTest, "func projectInitializeOriginCallerFixture(src string) (string, string) {\n", admissionInitShim, "84d470203f9f5cbcadef2d96bc49b49ec9efbd17"); label != "" {
		return label
	}
	if _, label := admissionStripShim(b1Test, "func b1Audit(s map[string]string) string {\n", admissionB1Shim, "2ee46df3d7031605bb95c6ddbb7c1a80b2d1c1f0"); label != "" {
		return label
	}
	wrapper := "func newSlog21Admitted(t *testing.T, choice constructorAdmissionChoice, selection ...expiryOwnerSelection) *slog21Fixture {\n\tt.Helper()\n\tprepareConstructorAdmission(t, choice)\n\treturn newSlog21(t, selection...)\n}"
	normal := "func admissionNormalSelection(scenario string) constructorAdmissionChoice {\n\tif scenario != \"ignored-body-query\" {\n\t\treturn constructorAdmissionChoice{}\n\t}\n\treturn newConstructorAdmission(admissionFamilyNormal, admissionNormalIgnored)\n}"
	boundary := "func admissionBoundarySelection(scenario string) constructorAdmissionChoice {\n\tif scenario != \"zero-rows\" {\n\t\treturn constructorAdmissionChoice{}\n\t}\n\treturn newConstructorAdmission(admissionFamilyBoundaries, admissionBoundaryZero)\n}"
	prepare := "func prepareConstructorAdmission(t *testing.T, choice constructorAdmissionChoice) {\n\tif choice == (constructorAdmissionChoice{}) {\n\t\treturn\n\t}\n\tt.Helper()\n\tchoice.plan.admit(choice)\n\tchoice.plan.closeWindow()\n\tt.Cleanup(func() {\n\t\tif t.Failed() {\n\t\t\tt.Log(\"B2_ADMISSION_PRIOR_FAILURE_NO_GO\")\n\t\t\treturn\n\t\t}\n\t\tv := choice.plan.view()\n\t\tif !v.admitted {\n\t\t\tt.Error(\"B2_ADMISSION_NOT_ACCEPTED\")\n\t\t\treturn\n\t\t}\n\t\tswitch v.selection {\n\t\tcase admissionNormalIgnored:\n\t\t\tt.Log(\"PRE_CONSTRUCTOR_ADMISSION_ONLY NORMAL_IGNORED\")\n\t\tcase admissionBoundaryZero:\n\t\t\tt.Log(\"PRE_CONSTRUCTOR_ADMISSION_ONLY BOUNDARIES_ZERO\")\n\t\tdefault:\n\t\t\tt.Error(\"B2_ADMISSION_SELECTION\")\n\t\t}\n\t})\n}"
	for _, rule := range [][2]string{{"newSlog21Admitted", wrapper}, {"admissionNormalSelection", normal}, {"admissionBoundarySelection", boundary}, {"prepareConstructorAdmission", prepare}} {
		if admissionFunction(self, rule[0]) != admissionExpectedFunction(rule[1]) {
			return "B2_ADMISSION_BRIDGE_SOURCE"
		}
	}
	if b1Audit(s) != "" || idocAudit(s["config.go"], fixture, s["expiry_invocation_owner_20261022_test.go"]) != "" || eioAudit(s["app.go"], s["subscription_expiry_worker.go"], fixture) != "" {
		return "B2_ADMISSION_INHERITED_GUARD"
	}
	return ""
}

func TestPR136ClientConfigurationGuardProjection(t *testing.T) {
	s := b1Sources(t)
	app, worker, fixture := s["app.go"], s["subscription_expiry_worker.go"], s["self_logout_confirmation_20261021_test.go"]
	self := b1Source(t, "self_logout_constructor_admission_20261022_test.go")
	b1Test := b1Source(t, "initialize_first_open_entry_relay_20261022_test.go")
	old, label := pr136ProjectApp(app)
	if label != "" || b1Blob(old) != "22444c3687485b71f58d9b367697c99acb9e7892" || eioAudit(app, worker, fixture) != "" || idocAudit(s["config.go"], fixture, s["expiry_invocation_owner_20261022_test.go"]) != "" || b1Audit(s) != "" || constructorAdmissionAudit(s, self, b1Test) != "" {
		t.Fatal("CI_GUARD_PR136_CURRENT_SOURCE")
	}
	models := "\tmux.HandleFunc(\"GET /v1/models\", a.listModels)\n"
	for _, mutation := range []struct{ name, source string }{
		{"route_missing", strings.Replace(app, pr136ClientConfigurationRoute, "", 1)},
		{"route_duplicate", strings.Replace(app, pr136ClientConfigurationRoute, pr136ClientConfigurationRoute+pr136ClientConfigurationRoute, 1)},
		{"route_method", strings.Replace(app, pr136ClientConfigurationRoute, strings.Replace(pr136ClientConfigurationRoute, "GET", "POST", 1), 1)},
		{"route_handler", strings.Replace(app, pr136ClientConfigurationRoute, strings.Replace(pr136ClientConfigurationRoute, "a.clientConfiguration", "a.listModels", 1), 1)},
		{"route_path", strings.Replace(app, pr136ClientConfigurationRoute, strings.Replace(pr136ClientConfigurationRoute, "client-configuration", "client-configuration-extra", 1), 1)},
		{"route_position", strings.Replace(app, models+pr136ClientConfigurationRoute, pr136ClientConfigurationRoute+models, 1)},
		{"route_extra", strings.Replace(app, pr136ClientConfigurationRoute, pr136ClientConfigurationRoute+"\tmux.HandleFunc(\"GET /v1/other-configuration\", a.clientConfiguration)\n", 1)},
		{"unrelated_app_change", strings.Replace(app, "\topened := false", "\topened := true", 1)},
	} {
		if !t.Run(mutation.name, func(t *testing.T) {
			mutant := b1Changed(s, "app.go", app, mutation.source)
			_, label := pr136ProjectApp(mutation.source)
			if mutation.source == app || label == "" || eioAudit(mutation.source, worker, fixture) == "" || b1Audit(mutant) == "" || constructorAdmissionAudit(mutant, self, b1Test) == "" {
				t.Fatal("CI_GUARD_PR136_APP_MUTATION_ACCEPTED")
			}
		}) {
			return
		}
	}
	for _, spec := range []struct{ kind, file, pin string }{
		{"owner", "expiry_invocation_owner_20261022_test.go", "82b469869a5ca87decd34e98f4acad477163c10da87cb638e783e66619876944"},
		{"origin", "initialize_driver_origin_caller_20261022_test.go", "b6172898796aa43f62469f38c83e6f941af4e4006cd04b40a58ab3c44f9a33cd"},
		{"b1", "initialize_first_open_entry_relay_20261022_test.go", "22444c3687485b71f58d9b367697c99acb9e7892"},
	} {
		src := b1Source(t, spec.file)
		_, shim, blob := pr136GuardSpec(spec.kind)
		projected, label := pr136RequireGuard(src, spec.kind)
		if label != "" || b1Blob(projected) != blob {
			t.Fatal("CI_GUARD_PR136_GUARD_PROJECTION")
		}
		for _, mutation := range []struct{ name, source string }{
			{"missing", strings.Replace(src, shim, "", 1)},
			{"duplicate", strings.Replace(src, shim, shim+shim, 1)},
			{"changed", strings.Replace(src, shim, strings.Replace(shim, "routeLabel", "differentLabel", 1), 1)},
			{"old_pin_refresh", strings.Replace(src, spec.pin, "changed", 1)},
		} {
			if !t.Run(spec.kind+"_"+mutation.name, func(t *testing.T) {
				_, label := pr136RequireGuard(mutation.source, spec.kind)
				if mutation.source == src || label == "" {
					t.Fatal("CI_GUARD_PR136_GUARD_MUTATION_ACCEPTED")
				}
				if spec.kind == "b1" {
					if constructorAdmissionAudit(s, self, mutation.source) == "" {
						t.Fatal("CI_GUARD_PR136_B1_MUTATION_ACCEPTED")
					}
				} else {
					mutant := b1Changed(s, spec.file, src, mutation.source)
					if b1Audit(mutant) == "" || constructorAdmissionAudit(mutant, self, b1Test) == "" || spec.kind == "owner" && idocAudit(s["config.go"], fixture, mutation.source) == "" {
						t.Fatal("CI_GUARD_PR136_INHERITED_MUTATION_ACCEPTED")
					}
				}
			}) {
				return
			}
		}
	}
}

func TestSelfLogoutConstructorAdmission20261022SourceMutants(t *testing.T) {
	s := b1Sources(t)
	self := b1Source(t, "self_logout_constructor_admission_20261022_test.go")
	b1Test := b1Source(t, "initialize_first_open_entry_relay_20261022_test.go")
	fixtureName := "self_logout_confirmation_20261021_test.go"
	old, label := projectConstructorAdmissionFixture(s[fixtureName])
	if label != "" {
		t.Fatal(label)
	}
	for _, m := range []struct{ name, file, from, to string }{
		{"missing-normal", fixtureName, admissionNormalCall, "\t\t\tf := newSlog21(t)\n"},
		{"missing-boundary", fixtureName, admissionBoundaryCall, "\t\t\tf := newSlog21(t, firstOwnerBoundarySelection(scenario))\n"},
		{"helper-only-baseline", fixtureName, s[fixtureName], old},
		{"wrong-family", fixtureName, "admissionNormalSelection(scenario)", "admissionBoundarySelection(scenario)"},
		{"drop-owner-forwarding", fixtureName, ", firstOwnerBoundarySelection(scenario))", ")"},
		{"third-wiring", fixtureName, "func TestSelfLogout20261021NativeAbort(t *testing.T) {\n\tf := newSlog21(t)", "func TestSelfLogout20261021NativeAbort(t *testing.T) {\n\tf := newSlog21Admitted(t, admissionNormalSelection(\"ignored-body-query\"))"},
		{"old-context", fixtureName, "context.WithTimeout(t.Context(), 30*time.Second)", "context.WithTimeout(t.Context(), 31*time.Second)"},
		{"old-error", fixtureName, "if initializeErr != nil {", "if false {"},
		{"old-cleanup", fixtureName, "\t\tf.prequery.report(t)\n", ""},
		{"old-initialize", fixtureName, "Initialize(f.initializeOriginStartup(ctx)", "Initialize(ctx"},
		{"old-auth-forwarding", fixtureName, "f.installFirstOwner(t, selection)", "f.installFirstOwner(t, nil)"},
		{"old-confirm-forwarding", fixtureName, "f.installInitializeOrigin(t, selection)", "f.installInitializeOrigin(t, nil)"},
		{"old-hash-refresh", "expiry_invocation_owner_20261022_test.go", "82b469869a5ca87decd34e98f4acad477163c10da87cb638e783e66619876944", "changed"},
		{"old-b1-caller", "initialize_driver_origin_caller_20261022_test.go", "armFirstOpenRelay(f.initializeOrigin, f.initializeDelivery)", "armFirstOpenRelay(nil, nil)"},
		{"old-regular", "app.go", "\topened := false", "\topened := true"},
	} {
		if !t.Run(m.name, func(t *testing.T) {
			mutant := b1Changed(s, m.file, m.from, m.to)
			if mutant[m.file] == s[m.file] || constructorAdmissionAudit(mutant, self, b1Test) == "" {
				t.Fatal("B2_ADMISSION_SOURCE_MUTATION_ACCEPTED")
			}
		}) {
			return
		}
	}
	for _, m := range []struct{ name, from, to string }{
		{"missing-admission", "\tprepareConstructorAdmission(t, choice)\n", ""},
		{"late-admission", "\tprepareConstructorAdmission(t, choice)\n\treturn newSlog21(t, selection...)", "\tf := newSlog21(t, selection...)\n\tprepareConstructorAdmission(t, choice)\n\treturn f"},
		{"duplicate-constructor", "\treturn newSlog21(t, selection...)", "\tnewSlog21(t, selection...)\n\treturn newSlog21(t, selection...)"},
		{"substitute-constructor", "\treturn newSlog21(t, selection...)", "\treturn &slog21Fixture{}"},
		{"shadow-constructor", "\treturn newSlog21(t, selection...)", "\tnewSlog21 := func(*testing.T, ...expiryOwnerSelection) *slog21Fixture { return nil }\n\treturn newSlog21(t, selection...)"},
		{"callback-constructor", "choice constructorAdmissionChoice, selection ...expiryOwnerSelection) *slog21Fixture {", "choice constructorAdmissionChoice, callback func() *slog21Fixture, selection ...expiryOwnerSelection) *slog21Fixture {"},
		{"drop-forwarding", "\treturn newSlog21(t, selection...)", "\treturn newSlog21(t)"},
		{"admit-after-close", "\tchoice.plan.admit(choice)\n\tchoice.plan.closeWindow()", "\tchoice.plan.closeWindow()\n\tchoice.plan.admit(choice)"},
		{"skip-on-metadata", "\tchoice.plan.admit(choice)", "\tif !choice.plan.admit(choice) { t.FailNow() }"},
		{"default-report", "\tif choice == (constructorAdmissionChoice{}) {\n\t\treturn\n\t}", "\tif choice == (constructorAdmissionChoice{}) {\n\t\tt.Cleanup(func() {})\n\t\treturn\n\t}"},
		{"wrong-selector", "return newConstructorAdmission(admissionFamilyNormal, admissionNormalIgnored)", "return newConstructorAdmission(admissionFamilyBoundaries, admissionBoundaryZero)"},
		{"suppress-old-primary", "\t\tif t.Failed() {", "\t\tif false {"},
	} {
		if !t.Run(m.name, func(t *testing.T) {
			mutant := strings.Replace(self, m.from, m.to, 1)
			if mutant == self || constructorAdmissionAudit(s, mutant, b1Test) == "" {
				t.Fatal("B2_ADMISSION_BRIDGE_MUTATION_ACCEPTED")
			}
		}) {
			return
		}
	}
	for _, m := range []struct{ name, source string }{
		{"b1-shim-missing", strings.Replace(b1Test, admissionB1Shim, "", 1)},
		{"b1-hash-refresh", strings.Replace(b1Test, "f6240407202ea487efdc68cb6034c7d970627619", "changed", 1)},
	} {
		if !t.Run(m.name, func(t *testing.T) {
			if constructorAdmissionAudit(s, self, m.source) == "" {
				t.Fatal("B2_ADMISSION_GUARD_MUTATION_ACCEPTED")
			}
		}) {
			return
		}
	}
}

func TestSelfLogoutConstructorAdmission20261022MappingDefault(t *testing.T) {
	if [3]uint8{uint8(admissionFamilyNone), uint8(admissionFamilyNormal), uint8(admissionFamilyBoundaries)} != [3]uint8{0, 1, 2} || [3]uint8{uint8(admissionUnselected), uint8(admissionNormalIgnored), uint8(admissionBoundaryZero)} != [3]uint8{0, 1, 2} || [4]uint8{uint8(admissionPhaseUnknown), uint8(admissionPhaseReady), uint8(admissionPhaseRecorded), uint8(admissionPhaseClosed)} != [4]uint8{0, 1, 2, 3} {
		t.Fatal("B2_ADMISSION_STATE_VALUES")
	}
	for _, scenario := range []string{"empty", "ignored-body-query", "begin-refusal", "begin-cancelled", "nil-result", "rows-error", "zero-rows", "multiple-rows", "employee-predicate", "verifier-predicate", "confirmation-query-error", "replacement-employee", "replacement-verifier", "pre-commit-refusal", "post-commit-report", "two-authenticated", "unknown", ""} {
		if !t.Run("map-"+scenario, func(t *testing.T) {
			normal, boundary := admissionNormalSelection(scenario), admissionBoundarySelection(scenario)
			if scenario == "ignored-body-query" {
				if normal.plan == nil || normal.family != admissionFamilyNormal || normal.selection != admissionNormalIgnored || normal.plan.view().n != 0 {
					t.Fatal("B2_ADMISSION_NORMAL_MAPPING")
				}
			} else if normal != (constructorAdmissionChoice{}) {
				t.Fatal("B2_ADMISSION_NORMAL_OVERSELECTED")
			}
			if scenario == "zero-rows" {
				if boundary.plan == nil || boundary.family != admissionFamilyBoundaries || boundary.selection != admissionBoundaryZero || boundary.plan.view().n != 0 {
					t.Fatal("B2_ADMISSION_BOUNDARY_MAPPING")
				}
			} else if boundary != (constructorAdmissionChoice{}) {
				t.Fatal("B2_ADMISSION_BOUNDARY_OVERSELECTED")
			}
		}) {
			return
		}
	}
	var zero *constructorAdmissionPlan
	if zero.view() != (constructorAdmissionView{}) || zero.admit(constructorAdmissionChoice{}) {
		t.Fatal("B2_ADMISSION_ZERO_POSITIVE")
	}
	zero.closeWindow()
	// A nil testing pointer proves default cannot call Helper or Cleanup.
	if n := testing.AllocsPerRun(20, func() {
		prepareConstructorAdmission(nil, admissionNormalSelection("empty"))
		prepareConstructorAdmission(nil, admissionBoundarySelection("begin-refusal"))
	}); n != 0 {
		t.Fatal("B2_ADMISSION_DEFAULT_ALLOCATION")
	}
}

func TestSelfLogoutConstructorAdmission20261022Metadata(t *testing.T) {
	for _, named := range []struct {
		name   string
		choice constructorAdmissionChoice
	}{{"normal", admissionNormalSelection("ignored-body-query")}, {"boundary", admissionBoundarySelection("zero-rows")}} {
		if !t.Run(named.name, func(t *testing.T) {
			choice := named.choice
			p := choice.plan
			if p.view().admitted || !p.admit(choice) || p.view().admitted || p.view().phase != admissionPhaseRecorded {
				t.Fatal("B2_ADMISSION_RECORD_NOT_ENTRY")
			}
			prefix := p.view()
			p.closeWindow()
			v := p.view()
			if !v.admitted || v.n != 1 || v.facts != prefix.facts || v != p.view() || v.family != choice.family || v.selection != choice.selection {
				t.Fatal("B2_ADMISSION_VALUE_FACT")
			}
			copy := v
			copy.facts[0] = constructorAdmissionFact{}
			if p.view() != v || copy == v {
				t.Fatal("B2_ADMISSION_VIEW_REFERENCE")
			}
			if p.admit(choice) || !p.view().invalid || p.view().admitted || p.view().facts != v.facts || p.view().n != v.n {
				t.Fatal("B2_ADMISSION_DUPLICATE_PREFIX")
			}
		}) {
			return
		}
	}
	for _, name := range []string{"nil-plan", "nil-token", "foreign-token", "copied-token", "foreign-plan", "foreign-receiver", "token-owner", "wrong-family", "wrong-selection", "third-family", "unknown-family", "third-selection", "unknown-selection", "unknown-phase", "late", "capacity"} {
		if !t.Run(name, func(t *testing.T) {
			c := admissionNormalSelection("ignored-body-query")
			p := c.plan
			other := admissionNormalSelection("ignored-body-query")
			switch name {
			case "nil-plan":
				c.plan = nil
			case "nil-token":
				c.token = nil
			case "foreign-token":
				c.token = other.token
			case "copied-token":
				copy := *c.token
				c.token = &copy
			case "foreign-plan":
				c.plan = other.plan
			case "foreign-receiver":
				p = other.plan
			case "token-owner":
				c.token.plan = other.plan
			case "wrong-family":
				c.family = admissionFamilyBoundaries
			case "wrong-selection":
				c.selection = admissionBoundaryZero
			case "third-family":
				c.family = 3
			case "unknown-family":
				c.family = 255
			case "third-selection":
				c.selection = 3
			case "unknown-selection":
				c.selection = 255
			case "unknown-phase":
				p.phase = 255
			case "late":
				p.phase = admissionPhaseClosed
			case "capacity":
				p.n = 1
				p.facts[0] = constructorAdmissionFact{family: admissionFamilyBoundaries, selection: admissionBoundaryZero}
			}
			before := p.view()
			if p.admit(c) || !p.view().invalid || p.view().admitted || p.view().facts != before.facts || p.view().n != before.n {
				t.Fatal("B2_ADMISSION_INVALID_ACCEPTED")
			}
			latched := p.view()
			p.closeWindow()
			p.admit(c)
			if p.view() != latched {
				t.Fatal("B2_ADMISSION_REJECTION_NOT_LATCHED")
			}
		}) {
			return
		}
	}
	for _, pair := range [][2]uint8{{0, 0}, {1, 2}, {2, 1}, {3, 1}, {1, 3}, {255, 255}} {
		c := newConstructorAdmission(constructorAdmissionFamily(pair[0]), constructorAdmissionSelection(pair[1]))
		if c.plan.admit(c) || !c.plan.view().invalid || c.plan.view().n != 0 {
			t.Fatal("B2_ADMISSION_INVALID_PAIR")
		}
	}
	c := admissionNormalSelection("ignored-body-query")
	c.plan.closeWindow()
	if c.plan.admit(c) || !c.plan.view().invalid || c.plan.view().n != 0 {
		t.Fatal("B2_ADMISSION_CLOSE_WITHOUT_RECORD")
	}
	c = admissionBoundarySelection("zero-rows")
	c.plan.admit(c)
	c.plan.closeWindow()
	before := c.plan.view()
	c.plan.closeWindow()
	if !c.plan.view().invalid || c.plan.view().admitted || c.plan.view().facts != before.facts {
		t.Fatal("B2_ADMISSION_DUPLICATE_CLOSE")
	}
}

func TestSelfLogoutConstructorAdmission20261022RealBridges(t *testing.T) {
	// Exactly two serial original lifetimes. No old test case body executes.
	// Each child's original cleanup completes before the next t.Run starts.
	if !t.Run("normal-ignored", func(t *testing.T) {
		choice := admissionNormalSelection("ignored-body-query")
		f := newSlog21Admitted(t, choice)
		if f == nil || f.firstOwner != nil || f.initializeOrigin != nil || f.initializeDelivery != nil || !choice.plan.view().admitted {
			t.Fatal("B2_ADMISSION_REAL_NORMAL")
		}
		t.Log("B2_REAL_PRE_CONSTRUCTOR_BRIDGE_CONFIRMED")
	}) {
		return
	}
	t.Run("boundaries-zero", func(t *testing.T) {
		choice := admissionBoundarySelection("zero-rows")
		selection := firstOwnerBoundarySelection("zero-rows")
		if selection != expiryOwnerUnselected {
			t.Fatal("B2_ADMISSION_LEGACY_SELECTION")
		}
		f := newSlog21Admitted(t, choice, selection)
		if f == nil || f.firstOwner != nil || f.initializeOrigin != nil || f.initializeDelivery != nil || !choice.plan.view().admitted {
			t.Fatal("B2_ADMISSION_REAL_BOUNDARY")
		}
		t.Log("B2_REAL_PRE_CONSTRUCTOR_BRIDGE_CONFIRMED")
	})
}
