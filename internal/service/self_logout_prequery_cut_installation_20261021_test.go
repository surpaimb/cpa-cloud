// Independently authored from the Gate4 installation contract, 2026-10-21.
// Test-only, private empty A view; no driver/financial/product identity bridge.
package service

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Owned only by the existing fixture goroutine; workers never receive it.
type slogPrequeryPlan struct {
	collector              *slogCollectorA
	view                   slogAViewToken
	armed, routed, invalid bool
	calls                  uint8
}

func newSlogPrequery() *slogPrequeryPlan {
	return &slogPrequeryPlan{collector: newSlogCollectorA(1, 1)}
}

func (p *slogPrequeryPlan) arm() {
	if p == nil {
		return
	}
	if p.armed {
		p.invalid = true
		return
	}
	p.armed = true
}

func (p *slogPrequeryPlan) route(stage slog21HandStage) *slogPrequeryPlan {
	if p == nil || !p.armed {
		return nil
	}
	return p.strictRoute(stage)
}

// Qualification is not strict invocation. Once selected, preserve every old
// rejection and latch; arm/report cannot clear or hide a true anomaly.
func (p *slogPrequeryPlan) strictRoute(stage slog21HandStage) *slogPrequeryPlan {
	if p == nil {
		return nil
	}
	if !p.armed || p.routed || stage != slog21HandFinalClean {
		p.invalid = true
		return nil
	}
	p.routed = true
	return p
}

// No T/log/SQL/timer/wait/seal/callback/allocation in the transaction window.
func (p *slogPrequeryPlan) cut() {
	if p == nil {
		return
	}
	if !p.armed || !p.routed || p.calls != 0 || p.collector == nil {
		p.invalid = true
		return
	}
	p.calls = 1
	p.view = p.collector.cut(0)
}

func (p *slogPrequeryPlan) status() string {
	if p == nil {
		return ""
	}
	if p.invalid {
		return "SPQI_INVALID_INSTALLATION"
	}
	if !p.armed || !p.routed || p.calls == 0 {
		return "SPQI_NOT_REACHED"
	}
	s := p.collector.summary(p.view)
	if !s.ready || s.h != 0 || s.scanned != 0 || s.faults != 0 || s.sealed || s.late || s.postSealUnknown {
		return "SPQI_INVALID_VIEW"
	}
	return "SPQI_REACHED_EMPTY_H0"
}

func (p *slogPrequeryPlan) report(t *testing.T) {
	if label := p.status(); label != "" {
		if label != "SPQI_REACHED_EMPTY_H0" {
			t.Error(label)
			return
		}
		t.Log(label)
	}
}

type spqiAnchor struct{ function, cut string }

// Independent frozen semantic order, NOT obtained from slog21DiagSites().
func spqiAnchors() []spqiAnchor {
	return []spqiAnchor{
		{"closedRaw", "slog21CutAfterClose"},
		{"openRaw", "slog21CutBeforeOpen"}, {"openRaw", "slog21CutAfterOpen"},
		{"reopen", "slog21CutReopenBeforeClose"},
		{"controlWindow", "slog21CutControlsBefore"}, {"controlWindow", "slog21CutControlsAfter"},
		{"install", "slog21CutTriggerBeforeClose"}, {"install", "slog21CutDDLBefore"}, {"install", "slog21CutDDLAfter"},
		{"clean", "slog21CutCleanupBefore"},
		{"TestSelfLogout20261021JointOracle", "slog21CutDeleteBefore"},
		{"TestSelfLogout20261021JointOracle", "slog21CutDeleteAfter"},
		{"TestSelfLogout20261021JointOracle", "slog21CutOracleTarget"},
		{"expectedAfter", "slog21CutPostResponse"}, {"expectedAfter", "slog21CutPersistedTarget"},
		{"TestSelfLogout20261021Normal", "slog21CutNormalBefore"},
		{"TestSelfLogout20261021NativeAbort", "slog21CutAbortBefore"},
		{"TestSelfLogout20261021Boundaries", "slog21CutBoundaryBefore"},
		{"TestSelfLogout20261021DeferredFK", "slog21CutFKBefore"},
		{"TestSelfLogout20261021AuthBoundaries", "slog21CutAuthBefore"},
		{"TestSelfLogout20261021AuthBoundaries", "slog21CutAuthAfter"},
		{"TestSelfLogout20261021TwoAuthenticated", "slog21CutDualBefore"},
	}
}

func spqiText(fs *token.FileSet, n ast.Node) string {
	var b bytes.Buffer
	if format.Node(&b, fs, n) != nil {
		return "INVALID"
	}
	return b.String()
}

func spqiFuncs(f *ast.File) map[string]*ast.FuncDecl {
	out := make(map[string]*ast.FuncDecl)
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok {
			out[fn.Name.Name] = fn
		}
	}
	return out
}

func spqiCall(e ast.Expr, name string) bool {
	c, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	if id, ok := c.Fun.(*ast.Ident); ok {
		return id.Name == name
	}
	if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
		return sel.Sel.Name == name
	}
	return false
}

func spqiIndex(fs *token.FileSet, body *ast.BlockStmt, text string) int {
	for i, s := range body.List {
		if spqiText(fs, s) == text {
			return i
		}
	}
	return -1
}

func spqiLeaf(f *ast.File) *ast.FuncLit {
	fn := spqiFuncs(f)["TestSelfLogout20261021Boundaries"]
	if fn == nil {
		return nil
	}
	var out *ast.FuncLit
	ast.Inspect(fn, func(n ast.Node) bool {
		if x, ok := n.(*ast.FuncLit); ok {
			out = x
			return false
		}
		return true
	})
	return out
}

func spqiSites(f *ast.File) []*ast.CompositeLit {
	fn := spqiFuncs(f)["slog21DiagSites"]
	if fn == nil {
		return nil
	}
	var out []*ast.CompositeLit
	ast.Inspect(fn, func(n ast.Node) bool {
		if c, ok := n.(*ast.CompositeLit); ok && len(c.Elts) == 3 {
			if kv, ok := c.Elts[0].(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "function" {
					out = append(out, c)
				}
			}
		}
		return true
	})
	return out
}

func spqiAudit(fs *token.FileSet, f *ast.File) string {
	fn := spqiFuncs(f)
	snap := fn["slog21Snapshot"]
	if snap == nil {
		return "SPQI_AST_INSTALLATION_MISSING"
	}
	counts := make(map[string]int)
	ast.Inspect(f, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			for _, name := range []string{"arm", "route", "cut", "report", "newSlogPrequery"} {
				if spqiCall(c, name) {
					counts[name]++
				}
			}
		}
		return true
	})
	if counts["cut"] == 0 {
		return "SPQI_AST_INSTALLATION_MISSING"
	}
	for _, name := range []string{"arm", "route", "cut", "report", "newSlogPrequery"} {
		if counts[name] != 1 {
			return "SPQI_AST_MULTIPLICITY"
		}
	}
	point := spqiIndex(fs, snap.Body, "if diag.handoff != nil {\n\tdiag.handoff.prequery.cut()\n}")
	objects := -1
	for i, s := range snap.Body.List {
		a, ok := s.(*ast.AssignStmt)
		if ok && len(a.Rhs) == 1 && spqiCall(a.Rhs[0], "slog21Read") {
			c := a.Rhs[0].(*ast.CallExpr)
			if len(c.Args) == 7 && spqiText(fs, c.Args[6]) == "slog21DiagObjects" {
				objects = i
			}
		}
	}
	if point < 2 || objects != point+1 ||
		spqiText(fs, snap.Body.List[point-2]) != "defer diag.rollback(t, ctx, tx)" ||
		spqiText(fs, snap.Body.List[point-1]) != "if diag.handoff != nil {\n\tdiag.handoff.observe(t, slog21HandBeginReturn, slog21HandOK, nil, -1, false)\n}" {
		return "SPQI_AST_CUT_POSITION"
	}
	raw := fn["closedRaw"]
	if raw == nil || spqiIndex(fs, raw.Body, "handoff.prequery = f.prequery.route(handoff.stage)") != 2 {
		return "SPQI_AST_ROUTE"
	}
	if spqiText(fs, raw.Body.List[3]) != "f.prepareSegments(handoff.stage, false)" {
		return "SPQI_AST_ROUTE"
	}
	clean := fn["clean"]
	if clean == nil {
		return "SPQI_AST_LIVE_APP"
	}
	for _, s := range clean.Body.List {
		if strings.Contains(spqiText(fs, s), "prequery") {
			return "SPQI_AST_LIVE_APP"
		}
	}
	leaf := spqiLeaf(f)
	if leaf == nil {
		return "SPQI_AST_ARM"
	}
	alloc := "if scenario == \"pre-commit-refusal\" {\n\tf.prequery = newSlogPrequery()\n}"
	arm := "if scenario == \"pre-commit-refusal\" {\n\tf.prequery.arm()\n}"
	ai := spqiIndex(fs, leaf.Body, arm)
	ei := spqiIndex(fs, leaf.Body, "f.expectedAfter(before, present)")
	ci := spqiIndex(fs, leaf.Body, "f.clean()")
	if spqiIndex(fs, leaf.Body, alloc) != 1 || ei < 0 || ai != ei+1 || ci != ai+1 {
		return "SPQI_AST_ARM"
	}
	var cleanup *ast.FuncLit
	if fixture := fn["newSlog21"]; fixture != nil {
		ast.Inspect(fixture, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && spqiCall(c, "Cleanup") && len(c.Args) == 1 {
				cleanup, _ = c.Args[0].(*ast.FuncLit)
			}
			return true
		})
	}
	if cleanup == nil || len(cleanup.Body.List) < 3 {
		return "SPQI_AST_REPORT"
	}
	end := cleanup.Body.List[len(cleanup.Body.List)-3:]
	for i, want := range []string{"f.flushSegments()", "f.flushExpirySQL()", "f.prequery.report(t)"} {
		if spqiText(fs, end[i]) != want {
			return "SPQI_AST_REPORT"
		}
	}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//line ") || strings.HasPrefix(c.Text, "/*line ") {
				return "SPQI_AST_LINE_DIRECTIVE"
			}
		}
	}
	var literals []string
	for _, d := range f.Decls {
		ast.Inspect(d, func(n ast.Node) bool {
			if l, ok := n.(*ast.BasicLit); ok && l.Kind == token.STRING && strings.HasPrefix(l.Value, "`") {
				literals = append(literals, l.Value)
			}
			return true
		})
	}
	if len(literals) != 54 || fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(literals, "\n")))) != "ceaa64a733c2b53af1111d34cd6b22c55d57bb3a47b97977beaac6ca5d104860" {
		return "SPQI_AST_FROZEN_RAW_LITERALS"
	}
	type callsite struct {
		function string
		line     int
	}
	var calls []callsite
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && spqiCall(c, "slog21Snapshot") {
				calls = append(calls, callsite{fd.Name.Name, fs.PositionFor(c.Pos(), false).Line})
			}
			return true
		})
	}
	wants, sites := spqiAnchors(), spqiSites(f)
	if len(calls) != 22 || len(sites) != 22 || len(wants) != 22 {
		return "SPQI_AST_ANCHOR_COUNT"
	}
	for i, want := range wants {
		vals := make(map[string]ast.Expr)
		for _, e := range sites[i].Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				vals[spqiText(fs, kv.Key)] = kv.Value
			}
		}
		name, err := strconv.Unquote(spqiText(fs, vals["function"]))
		line, lineErr := strconv.Atoi(spqiText(fs, vals["line"]))
		full := want.function
		if !strings.HasPrefix(full, "Test") {
			full = "(*slog21Fixture)." + full
		}
		if err != nil || lineErr != nil || name != full || spqiText(fs, vals["cut"]) != want.cut || calls[i].function != want.function || line != calls[i].line {
			return "SPQI_AST_ANCHOR_MISMATCH"
		}
	}
	return ""
}

func spqiSource(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("self_logout_confirmation_20261021_test.go")
	if err != nil {
		t.Fatal("SPQI_SOURCE_READ")
	}
	return b
}

func spqiParse(t *testing.T, b []byte) (*token.FileSet, *ast.File) {
	t.Helper()
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "frozen.go", b, parser.ParseComments)
	if err != nil {
		t.Fatal("SPQI_SOURCE_PARSE")
	}
	return fs, f
}

// Full existing fixture prefix, not just a post-arm synthetic suffix. This
// control uses exactly the qualification primitive called by closedRaw.
func TestSelfLogoutPrequery20261021LifecyclePrefix(t *testing.T) {
	p := newSlogPrequery()
	if p.route(slog21HandReopen) != nil || p.invalid || p.status() != "SPQI_NOT_REACHED" {
		t.Fatal("SPQI_LIFECYCLE_PREARM_REOPEN")
	}
	p.arm()
	if p.route(slog21HandFinalClean) != p {
		t.Fatal("SPQI_LIFECYCLE_SELECTED_ROUTE")
	}
	p.cut()
	if p.status() != "SPQI_REACHED_EMPTY_H0" || p.collector.gate.Load() != 0 {
		t.Fatal("SPQI_LIFECYCLE_EMPTY_CUT")
	}
}

func TestSelfLogoutPrequery20261021Source(t *testing.T) {
	fs, f := spqiParse(t, spqiSource(t))
	if label := spqiAudit(fs, f); label != "" {
		t.Fatal(label)
	}
}

func TestSelfLogoutPrequery20261021Plan(t *testing.T) {
	t.Run("empty-once-immutable", func(t *testing.T) {
		p := newSlogPrequery()
		p.arm()
		r := p.route(slog21HandFinalClean)
		if r != p {
			t.Fatal("SPQI_CONTROL_ROUTE")
		}
		r.cut()
		before := p.collector.summary(p.view)
		if p.status() != "SPQI_REACHED_EMPTY_H0" || p.status() != "SPQI_REACHED_EMPTY_H0" || p.collector.summary(p.view) != before || p.collector.gate.Load() != 0 {
			t.Fatal("SPQI_CONTROL_EMPTY_IMMUTABLE")
		}
	})
	t.Run("not-reached", func(t *testing.T) {
		p := newSlogPrequery()
		if p.status() != "SPQI_NOT_REACHED" {
			t.Fatal("SPQI_CONTROL_NOT_REACHED")
		}
		p.arm()
		if p.status() != "SPQI_NOT_REACHED" {
			t.Fatal("SPQI_CONTROL_ARM_NOT_CUT")
		}
		p.route(slog21HandFinalClean)
		if p.status() != "SPQI_NOT_REACHED" {
			t.Fatal("SPQI_CONTROL_ROUTE_NOT_CUT")
		}
	})
	for _, stage := range []slog21HandStage{0, slog21HandInstall, slog21HandReopen} {
		t.Run(fmt.Sprintf("wrong-stage-%d", stage), func(t *testing.T) {
			p := newSlogPrequery()
			p.arm()
			if p.route(stage) != nil || p.status() != "SPQI_INVALID_INSTALLATION" || p.calls != 0 {
				t.Fatal("SPQI_CONTROL_WRONG_STAGE")
			}
		})
	}
	for _, kind := range []string{"before-arm", "before-route", "duplicate-arm", "duplicate-route", "duplicate-cut"} {
		t.Run(kind, func(t *testing.T) {
			p := newSlogPrequery()
			switch kind {
			case "before-arm":
				p.strictRoute(slog21HandFinalClean)
			case "before-route":
				p.arm()
				p.cut()
			case "duplicate-arm":
				p.arm()
				p.arm()
			case "duplicate-route":
				p.arm()
				p.route(slog21HandFinalClean)
				p.route(slog21HandFinalClean)
			case "duplicate-cut":
				p.arm()
				p.route(slog21HandFinalClean)
				p.cut()
				p.cut()
			}
			if p.status() != "SPQI_INVALID_INSTALLATION" {
				t.Fatal("SPQI_CONTROL_LATCH")
			}
		})
	}
	t.Run("nil-opt-out", func(t *testing.T) {
		var p *slogPrequeryPlan
		p.arm()
		p.cut()
		p.report(t)
		if p.route(slog21HandFinalClean) != nil || p.status() != "" {
			t.Fatal("SPQI_CONTROL_NIL")
		}
	})
}

func spqiQualificationAST(fs *token.FileSet, f *ast.File) bool {
	fn := spqiFuncs(f)["route"]
	return fn != nil && len(fn.Body.List) == 2 &&
		spqiText(fs, fn.Body.List[0]) == "if p == nil || !p.armed {\n\treturn nil\n}" &&
		spqiText(fs, fn.Body.List[1]) == "return p.strictRoute(stage)"
}

func TestSelfLogoutPrequery20261021QualificationSafety(t *testing.T) {
	t.Run("missed-arm-not-reached", func(t *testing.T) {
		p := newSlogPrequery()
		if p.route(slog21HandReopen) != nil || p.route(slog21HandFinalClean) != nil ||
			p.status() != "SPQI_NOT_REACHED" || p.invalid || p.routed || p.calls != 0 || p.view.owner != nil || p.collector.gate.Load() != 0 {
			t.Fatal("SPQI_QUALIFICATION_MISSED_ARM")
		}
	})
	t.Run("strict-anomaly-survives-later-arm", func(t *testing.T) {
		p := newSlogPrequery()
		if p.strictRoute(slog21HandReopen) != nil || !p.invalid {
			t.Fatal("SPQI_QUALIFICATION_STRICT_BYPASS")
		}
		p.arm()
		p.route(slog21HandFinalClean).cut()
		if p.status() != "SPQI_INVALID_INSTALLATION" || !p.invalid {
			t.Fatal("SPQI_QUALIFICATION_ANOMALY_ERASED")
		}
	})
	t.Run("deleting-qualification-gate-is-rejected", func(t *testing.T) {
		b, err := os.ReadFile("self_logout_prequery_cut_installation_20261021_test.go")
		if err != nil {
			t.Fatal("SPQI_QUALIFICATION_SOURCE_READ")
		}
		fs, f := spqiParse(t, b)
		if !spqiQualificationAST(fs, f) {
			t.Fatal("SPQI_QUALIFICATION_GATE_MISSING")
		}
		fn := spqiFuncs(f)["route"]
		fn.Body.List = fn.Body.List[1:]
		if spqiQualificationAST(fs, f) {
			t.Fatal("SPQI_QUALIFICATION_DELETION_ACCEPTED")
		}
	})
}

func TestSelfLogoutPrequery20261021Mutations(t *testing.T) {
	b := spqiSource(t)
	mutations := []string{"missing", "misplaced", "duplicate", "early-arm", "live-App", "wrong-route", "early-report", "23rd-snapshot", "raw-literal", "cut-enum"}
	for _, kind := range mutations {
		t.Run(kind, func(t *testing.T) {
			fs, f := spqiParse(t, b)
			if spqiAudit(fs, f) != "" {
				t.Fatal("SPQI_MUTATION_BASELINE")
			}
			fn := spqiFuncs(f)
			snap := fn["slog21Snapshot"].Body
			idx := spqiIndex(fs, snap, "if diag.handoff != nil {\n\tdiag.handoff.prequery.cut()\n}")
			switch kind {
			case "missing":
				snap.List = append(snap.List[:idx], snap.List[idx+1:]...)
			case "misplaced":
				snap.List[idx], snap.List[idx-1] = snap.List[idx-1], snap.List[idx]
			case "duplicate":
				snap.List = append(snap.List, snap.List[idx])
			case "early-arm":
				body := spqiLeaf(f).Body
				i := spqiIndex(fs, body, "if scenario == \"pre-commit-refusal\" {\n\tf.prequery.arm()\n}")
				body.List[i], body.List[i-1] = body.List[i-1], body.List[i]
			case "live-App":
				fn["clean"].Body.List = append(fn["clean"].Body.List, snap.List[idx])
			case "wrong-route":
				a := fn["closedRaw"].Body.List[2].(*ast.AssignStmt)
				a.Rhs[0].(*ast.CallExpr).Args[0] = ast.NewIdent("slog21HandReopen")
			case "early-report":
				ast.Inspect(fn["newSlog21"], func(n ast.Node) bool {
					if c, ok := n.(*ast.CallExpr); ok && spqiCall(c, "Cleanup") {
						body := c.Args[0].(*ast.FuncLit).Body
						n := len(body.List)
						body.List[n-1], body.List[n-2] = body.List[n-2], body.List[n-1]
					}
					return true
				})
			case "23rd-snapshot":
				x, _ := parser.ParseExpr("slog21Snapshot(t, db)")
				fn["clean"].Body.List = append(fn["clean"].Body.List, &ast.ExprStmt{X: x})
			case "raw-literal":
				done := false
				ast.Inspect(f, func(n ast.Node) bool {
					if l, ok := n.(*ast.BasicLit); ok && !done && strings.HasPrefix(l.Value, "`") {
						l.Value = "`changed`"
						done = true
					}
					return true
				})
			case "cut-enum":
				spqiSites(f)[0].Elts[2].(*ast.KeyValueExpr).Value = ast.NewIdent("slog21CutBeforeOpen")
			}
			if spqiAudit(fs, f) == "" {
				t.Fatal("SPQI_MUTATION_ACCEPTED")
			}
		})
	}
	for i := range spqiAnchors() {
		t.Run(fmt.Sprintf("physical-anchor-%02d", i+1), func(t *testing.T) {
			fs, f := spqiParse(t, b)
			kv := spqiSites(f)[i].Elts[1].(*ast.KeyValueExpr)
			kv.Value = &ast.BasicLit{Kind: token.INT, Value: "1"}
			if spqiAudit(fs, f) != "SPQI_AST_ANCHOR_MISMATCH" {
				t.Fatal("SPQI_MUTATION_ANCHOR_NOT_REJECTED")
			}
		})
	}
}
