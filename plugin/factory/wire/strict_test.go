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

	"github.com/the-protobuf-project/orm/plugin/factory/source/proto/backend"
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

// TestMisappliedPresetAlwaysFails covers the one validation problem that is not
// strict-gated. The recoverable problems above degrade to something sensible — a
// soft FK, a dropped index — so tolerating them by default is reasonable. A
// preset that cannot apply to its column has no such fallback: it would simply
// vanish, leaving the author believing the column is validated. So it fails at
// the default strict spec, and the message names every offender at once.
func TestMisappliedPresetAlwaysFails(t *testing.T) {
	err := generateStrict(t, "testdata/strict/bad_preset", "")
	if err == nil {
		t.Fatal("generate succeeded with misapplied presets, want error")
	}
	for _, want := range []string{
		"quantity",              // the numeric column
		"VALIDATE_EMAIL",        // what was misapplied there
		"string columns",        // why it cannot apply
		"labels",                // the repeated column
		"VALIDATE_LOWERCASE",    // what was misapplied there
		"not a repeated column", // why it cannot apply
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// TestIncoherentCachePolicyAlwaysFails is the cache counterpart: a key over a
// column that does not exist can never be built, and a stream invalidation with
// no subject can never fire. Both are inert rather than wrong, which is exactly
// why they have to fail loudly.
func TestIncoherentCachePolicyAlwaysFails(t *testing.T) {
	err := generateStrict(t, "testdata/strict/bad_cache", "")
	if err == nil {
		t.Fatal("generate succeeded with an incoherent cache policy, want error")
	}
	for _, want := range []string{
		"nonexistent_column",  // the key over a column that is not there
		"INVALIDATION_STREAM", // the invalidation that cannot fire
		"no stream subject",   // why it cannot fire
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// TestIncoherentConstraintAlwaysFails is the parameterized-rule counterpart. The
// fractional-bound case is the interesting one: it would not vanish, it would
// emit a Go constant that does not convert, turning a schema mistake into a
// compile error in the user's generated tree. Catching it here keeps the failure
// where it can be understood.
func TestIncoherentConstraintAlwaysFails(t *testing.T) {
	err := generateStrict(t, "testdata/strict/bad_constraint", "")
	if err == nil {
		t.Fatal("generate succeeded with incoherent constraints, want error")
	}
	for _, want := range []string{
		"min 100 is greater than max 10", // unsatisfiable bound
		"is fractional",                  // 1.5 on an int32
		"does not compile",               // the broken pattern
		"in/not_in apply to string",      // a value set on a numeric column
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
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
	return protokit.Run(p, protokit.Options{Target: "sql", Strict: strict}, wire.ProtoTargets(), backend.Backend{})
}
