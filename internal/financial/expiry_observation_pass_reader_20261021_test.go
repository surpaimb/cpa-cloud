package financial

// Independently authored pure controls for the 2026-10-21 pass-reader contract.
// No App/DB/SQL/worker/driver call, identity admission or private test export.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
	"time"
)

func eprNever(ExpiryObservation) { panic("EPR_OBSERVER_CALLED") }

func eprCheck(t *testing.T, ctx context.Context, state ExpiryPassReadState, raw uint16) {
	t.Helper()
	if ReadExpiryObservationPass(ctx) != (ExpiryPassRead{State: state, RawPass: raw}) {
		t.Fatal("EPR_RESULT_MISMATCH")
	}
}

func TestExpiryPassReader20261021TestsFirstOverLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		pass uint16
	}{{"129", 129}, {"65535", 65535}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithExpiryObservation(context.Background(), eprNever, tc.pass)
			if ReadExpiryObservationPass(ctx) != (ExpiryPassRead{State: ExpiryPassOverLimit, RawPass: tc.pass}) {
				t.Fatal("EPR_TESTS_FIRST_OVERLIMIT_RAW")
			}
		})
	}
}

func TestExpiryPassReader20261021Shapes(t *testing.T) {
	var typedNil *expiryObservationBinding
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		state ExpiryPassReadState
		raw   uint16
	}{
		{"nil-context", nil, ExpiryPassNoContext, 0},
		{"absent", context.Background(), ExpiryPassAbsent, 0},
		{"wrong-int", context.WithValue(context.Background(), expiryObservationKey{}, 1), ExpiryPassInvalid, 0},
		{"wrong-string", context.WithValue(context.Background(), expiryObservationKey{}, "synthetic-wrong-type"), ExpiryPassInvalid, 0},
		{"typed-nil-binding", context.WithValue(context.Background(), expiryObservationKey{}, typedNil), ExpiryPassInvalid, 0},
		{"pointer-not-value", context.WithValue(context.Background(), expiryObservationKey{}, &expiryObservationBinding{observer: eprNever, pass: 1}), ExpiryPassInvalid, 0},
		{"nil-observer", context.WithValue(context.Background(), expiryObservationKey{}, expiryObservationBinding{pass: 1}), ExpiryPassInvalid, 0},
		{"nil-observer-before-range", context.WithValue(context.Background(), expiryObservationKey{}, expiryObservationBinding{pass: 129}), ExpiryPassInvalid, 0},
		{"zero-pass", WithExpiryObservation(context.Background(), eprNever, 0), ExpiryPassInvalid, 0},
		{"one", WithExpiryObservation(context.Background(), eprNever, 1), ExpiryPassInRangeUnattributed, 1},
		{"128", WithExpiryObservation(context.Background(), eprNever, 128), ExpiryPassInRangeUnattributed, 128},
		{"129", WithExpiryObservation(context.Background(), eprNever, 129), ExpiryPassOverLimit, 129},
		{"65535", WithExpiryObservation(context.Background(), eprNever, 65535), ExpiryPassOverLimit, 65535},
	} {
		t.Run(tc.name, func(t *testing.T) { eprCheck(t, tc.ctx, tc.state, tc.raw) })
	}
}

// Only Value may be used. Other methods fail if the reader inspects lifecycle.
type eprValueOnlyContext struct {
	lookups int
	value   any
}

func (*eprValueOnlyContext) Deadline() (time.Time, bool) { panic("EPR_DEADLINE_CALLED") }
func (*eprValueOnlyContext) Done() <-chan struct{}       { panic("EPR_DONE_CALLED") }
func (*eprValueOnlyContext) Err() error                  { panic("EPR_ERR_CALLED") }
func (c *eprValueOnlyContext) Value(key any) any {
	if _, ok := key.(expiryObservationKey); !ok {
		panic("EPR_WRONG_LOOKUP_KEY")
	}
	c.lookups++
	return c.value
}

func TestExpiryPassReader20261021SingleLookupAndCopy(t *testing.T) {
	c := &eprValueOnlyContext{value: expiryObservationBinding{observer: eprNever, pass: 128}}
	for i := 0; i < 3; i++ {
		r := ReadExpiryObservationPass(c)
		if c.lookups != i+1 || r != (ExpiryPassRead{State: ExpiryPassInRangeUnattributed, RawPass: 128}) {
			t.Fatal("EPR_SINGLE_VALUE_LOOKUP")
		}
		r.State, r.RawPass = ExpiryPassInvalid, 0 // returned copy cannot mutate binding
	}
	eprCheck(t, c, ExpiryPassInRangeUnattributed, 128)
	if c.lookups != 4 {
		t.Fatal("EPR_COPY_READ_COUNT")
	}
}

func TestExpiryPassReader20261021CancellationAndValues(t *testing.T) {
	type unrelatedKey struct{}
	parent := context.WithValue(context.Background(), unrelatedKey{}, "synthetic-unrelated")
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	bound := WithExpiryObservation(ctx, eprNever, 7)
	done := bound.Done()
	deadline, hasDeadline := bound.Deadline()
	eprCheck(t, bound, ExpiryPassInRangeUnattributed, 7)
	if bound.Done() != done || bound.Err() != nil || bound.Value(unrelatedKey{}) != "synthetic-unrelated" {
		t.Fatal("EPR_ACTIVE_CONTEXT_CHANGED")
	}
	cause := errors.New("synthetic-public-cancellation")
	cancel(cause)
	eprCheck(t, bound, ExpiryPassInRangeUnattributed, 7)
	afterDeadline, afterHasDeadline := bound.Deadline()
	if bound.Done() != done || bound.Err() != context.Canceled || context.Cause(bound) != cause ||
		hasDeadline != afterHasDeadline || !deadline.Equal(afterDeadline) || bound.Value(unrelatedKey{}) != "synthetic-unrelated" {
		t.Fatal("EPR_CANCELLED_CONTEXT_CHANGED")
	}
	eprCheck(t, parent, ExpiryPassAbsent, 0)
}

func TestExpiryPassReader20261021Nested(t *testing.T) {
	outer := WithExpiryObservation(context.Background(), eprNever, 7)
	for _, tc := range []struct {
		name  string
		inner context.Context
		state ExpiryPassReadState
		raw   uint16
	}{
		{"valid-inner", WithExpiryObservation(outer, eprNever, 128), ExpiryPassInRangeUnattributed, 128},
		{"zero-shadows", WithExpiryObservation(outer, eprNever, 0), ExpiryPassInvalid, 0},
		{"overlimit-shadows", WithExpiryObservation(outer, eprNever, 129), ExpiryPassOverLimit, 129},
		{"max-shadows", WithExpiryObservation(outer, eprNever, 65535), ExpiryPassOverLimit, 65535},
		{"wrong-type-shadows", context.WithValue(outer, expiryObservationKey{}, 42), ExpiryPassInvalid, 0},
		{"nil-observer-binding-shadows", context.WithValue(outer, expiryObservationKey{}, expiryObservationBinding{pass: 7}), ExpiryPassInvalid, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eprCheck(t, tc.inner, tc.state, tc.raw)
			eprCheck(t, outer, ExpiryPassInRangeUnattributed, 7)
		})
	}
	t.Run("nil-observer-writer-preserves-outer", func(t *testing.T) {
		inner := WithExpiryObservation(outer, nil, 129)
		if inner != outer {
			t.Fatal("EPR_OLD_WRITER_IDENTITY_CHANGED")
		}
		eprCheck(t, inner, ExpiryPassInRangeUnattributed, 7)
		if WithExpiryObservation(nil, eprNever, 1) != nil {
			t.Fatal("EPR_OLD_WRITER_NIL_CHANGED")
		}
	})
}

func eprSourceOK(f *ast.File) bool {
	if f.Name.Name != "financial" || len(f.Imports) != 1 || f.Imports[0].Path.Value != "\"context\"" {
		return false
	}
	functions, calls, constants, types := 0, 0, 0, 0
	wantNames := []string{"ExpiryPassNoContext", "ExpiryPassAbsent", "ExpiryPassInvalid", "ExpiryPassInRangeUnattributed", "ExpiryPassOverLimit"}
	ok := true
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			functions++
			if d.Name.Name != "ReadExpiryObservationPass" || d.Recv != nil {
				ok = false
			}
		case *ast.GenDecl:
			if d.Tok == token.VAR {
				ok = false
			}
			for _, s := range d.Specs {
				if v, yes := s.(*ast.ValueSpec); yes {
					if d.Tok != token.CONST || len(v.Names) != 1 || constants >= len(wantNames) || v.Names[0].Name != wantNames[constants] {
						ok = false
					}
					constants++
				}
				if typ, yes := s.(*ast.TypeSpec); yes {
					types++
					switch typ.Name.Name {
					case "ExpiryPassReadState":
						id, yes := typ.Type.(*ast.Ident)
						if !yes || id.Name != "uint8" {
							ok = false
						}
					case "ExpiryPassRead":
						st, yes := typ.Type.(*ast.StructType)
						if !yes || len(st.Fields.List) != 2 {
							ok = false
							continue
						}
						for i, field := range st.Fields.List {
							name, typ := []string{"State", "RawPass"}[i], []string{"ExpiryPassReadState", "uint16"}[i]
							id, yes := field.Type.(*ast.Ident)
							if len(field.Names) != 1 || field.Names[0].Name != name || !yes || id.Name != typ || field.Tag != nil {
								ok = false
							}
						}
					default:
						ok = false
					}
				}
			}
		default:
			ok = false
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			calls++
			sel, yes := n.Fun.(*ast.SelectorExpr)
			if !yes || sel.Sel.Name != "Value" || len(n.Args) != 1 {
				ok = false
				break
			}
			id, yes := sel.X.(*ast.Ident)
			if !yes || id.Name != "ctx" {
				ok = false
			}
			key, yes := n.Args[0].(*ast.CompositeLit)
			if !yes || len(key.Elts) != 0 {
				ok = false
				break
			}
			id, yes = key.Type.(*ast.Ident)
			if !yes || id.Name != "expiryObservationKey" {
				ok = false
			}
		case *ast.GoStmt, *ast.DeferStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SelectStmt:
			ok = false
		}
		return true
	})
	return ok && functions == 1 && calls == 1 && constants == 5 && types == 2
}

func TestExpiryPassReader20261021RegularSourceBoundary(t *testing.T) {
	b, err := os.ReadFile("expiry_observation_pass_reader_20261021.go")
	if err != nil {
		t.Fatal("EPR_REGULAR_SOURCE_READ")
	}
	f, err := parser.ParseFile(token.NewFileSet(), "reader.go", b, 0)
	if err != nil || !eprSourceOK(f) {
		t.Fatal("EPR_REGULAR_SOURCE_BOUNDARY")
	}
	for _, kind := range []string{"other-context-method", "extra-field", "mutable-global"} {
		t.Run(kind, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), "reader.go", b, 0)
			if err != nil {
				t.Fatal("EPR_SOURCE_MUTATION_PARSE")
			}
			switch kind {
			case "other-context-method":
				ast.Inspect(f, func(n ast.Node) bool {
					if s, ok := n.(*ast.SelectorExpr); ok && s.Sel.Name == "Value" {
						s.Sel.Name = "Err"
					}
					return true
				})
			case "extra-field":
				ast.Inspect(f, func(n ast.Node) bool {
					if s, ok := n.(*ast.StructType); ok {
						s.Fields.List = append(s.Fields.List, &ast.Field{Names: []*ast.Ident{ast.NewIdent("Observer")}, Type: &ast.FuncType{Params: &ast.FieldList{}}})
					}
					return true
				})
			case "mutable-global":
				f.Decls = append(f.Decls, &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{&ast.ValueSpec{Names: []*ast.Ident{ast.NewIdent("currentPass")}, Type: ast.NewIdent("uint16")}}})
			}
			if eprSourceOK(f) {
				t.Fatal("EPR_SOURCE_MUTATION_ACCEPTED")
			}
		})
	}
}
