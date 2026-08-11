package wire_test

// Strict-mode tests: schema problems that are recoverable warnings by default
// (codegen proceeds with a fallback) must become hard errors under Options.Strict.
// Fixtures live under testdata/strict/ (not testdata/cases/, so TestGolden
// ignores them) and are compiled with the same in-process harness as the golden
// tests. Driven through the sql database target.

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/orm/plugin/factory/wire"
	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/golden"
)

func TestStrictMode(t *testing.T) {
	cases := []struct {
		name     string
		dir      string
		rule     string // the rule the problem is filed under
		mentions string // substring the strict error must contain
	}{
		{"unresolved_fk", "testdata/strict/unresolved_fk", "ref", "Ghost"},
		{"bad_index", "testdata/strict/bad_index", "index", "nonexistent_column"},
		{"lint", "testdata/strict/lint", "lint", "disagrees with package"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Default ("") tolerates every problem and generation succeeds.
			if err := generateStrict(t, c.dir, ""); err != nil {
				t.Fatalf("non-strict generate failed: %v", err)
			}
			// strict=true promotes every rule to an error.
			err := generateStrict(t, c.dir, "true")
			if err == nil {
				t.Fatal("strict=true generate succeeded, want error")
			}
			if !strings.Contains(err.Error(), c.mentions) {
				t.Errorf("strict error does not mention %q: %v", c.mentions, err)
			}
			// Granular: erroring only this case's rule fails, while erroring a
			// different rule still succeeds.
			if err := generateStrict(t, c.dir, c.rule+":error"); err == nil {
				t.Errorf("strict=%s:error succeeded, want error", c.rule)
			}
			if err := generateStrict(t, c.dir, "collision:error"); err != nil {
				t.Errorf("strict=collision:error should not fail a %s problem: %v", c.rule, err)
			}
		})
	}
}

// generateStrict compiles the case protos and runs the sql target with the given
// strict spec, returning the generation error (if any).
func generateStrict(t *testing.T, dir, strict string) error {
	t.Helper()
	req := golden.BuildRequest(t, dir)
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen: %v", err)
	}
	return protokit.RunPlugin(p, protokit.Options{Target: "sql", Strict: strict}, ormPlugin())
}
