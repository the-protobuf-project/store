package gorm

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/the-protobuf-project/orm/plugin/factory/target/validate"
	"github.com/the-protobuf-project/protokit/schema"
)

// renderValidatex renders the shared runtime the way Generate does. renderGo
// gofmts, so a syntax error in the template fails here rather than in a golden.
func renderValidatex(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := renderGo(&buf, "validatex.go.tpl", validatexView(&schema.Database{Name: "test"})); err != nil {
		t.Fatalf("render validatex.go.tpl: %v", err)
	}
	return buf.String()
}

// TestValidatexDefinesEveryPredicate is the parity guard between the projection
// table and the generated runtime. validate.Rule.Func names a function the
// models' Validate methods will call; if the template does not declare it, the
// generated tree fails to compile at the user's end. Catching it here keeps that
// failure inside this repo's test suite.
func TestValidatexDefinesEveryPredicate(t *testing.T) {
	src := renderValidatex(t)
	f, err := parser.ParseFile(token.NewFileSet(), "validatex.go", src, 0)
	if err != nil {
		t.Fatalf("parse rendered validatex: %v", err)
	}

	declared := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if ok && fn.Recv == nil {
			declared[fn.Name.Name] = true
		}
	}

	for _, name := range validate.FuncNames() {
		if !declared[name] {
			t.Errorf("validate table names predicate %q, but validatex.go.tpl declares no such function", name)
		}
	}
}

// TestValidatexIsStdlibOnly guards the reason this runtime exists: generated
// output must not drag a validation dependency into a consumer's build.
func TestValidatexIsStdlibOnly(t *testing.T) {
	src := renderValidatex(t)
	f, err := parser.ParseFile(token.NewFileSet(), "validatex.go", src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse rendered validatex: %v", err)
	}
	for _, imp := range f.Imports {
		path := imp.Path.Value
		// A third-party path is distinguishable by its dotted first segment
		// (github.com/..., gorm.io/...); stdlib paths have none.
		if bytes.Contains([]byte(path[:len(path)-1]), []byte(".")) {
			t.Errorf("validatex imports %s — the runtime must stay stdlib-only", path)
		}
	}
}
