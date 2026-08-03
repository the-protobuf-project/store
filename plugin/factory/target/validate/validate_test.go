package validate

import (
	"strings"
	"testing"

	"github.com/the-protobuf-project/orm/plugin/pb/ormpbv1"
)

// TestEveryPresetProjects is the guard that matters: adding a value to
// orm.v1.Validate without a projection here would silently generate a schema
// that ignores the annotation. Enumerating the proto's own value descriptors
// keeps this honest — a new preset fails the build instead of no-op'ing.
func TestEveryPresetProjects(t *testing.T) {
	values := ormpbv1.Validate(0).Descriptor().Values()
	for i := range values.Len() {
		v := values.Get(i)
		preset := ormpbv1.Validate(v.Number())
		if preset == ormpbv1.Validate_VALIDATE_UNSPECIFIED {
			continue
		}
		r, ok := rules[preset]
		if !ok {
			t.Errorf("%s: declared in orm.v1 but has no projection in rules", v.Name())
			continue
		}
		if r.Func == "" {
			t.Errorf("%s: no validatex predicate — every preset must be enforceable app-side", v.Name())
		}
		if r.Message == "" {
			t.Errorf("%s: no failure message", v.Name())
		}
		if !r.List && len(r.Kinds) == 0 {
			t.Errorf("%s: no applicable field types, so it can never be accepted", v.Name())
		}
		if r.SQL != "" && !strings.Contains(r.SQL, "{}") {
			t.Errorf("%s: SQL %q has no {} column placeholder", v.Name(), r.SQL)
		}
		if r.Preset != preset {
			t.Errorf("%s: Preset is %v, want %v — constraint names would be wrong", v.Name(), r.Preset, preset)
		}
		if slug := r.Slug(); slug == "" || strings.HasPrefix(slug, "validate") {
			t.Errorf("%s: slug %q is not a bare preset name", v.Name(), slug)
		}
	}
}

// TestRuleSQLHasNoBackslash guards a failure mode that is invisible in the
// generated source and only shows up against a live database. These expressions
// are embedded in GORM struct tags; reflect.StructTag.Get unquotes a tag value as
// a Go string literal, so a `\.` is an invalid escape, the unquote fails, and the
// whole gorm tag for that field is silently dropped — the column then migrates
// without its NOT NULL or its type, not merely without its CHECK. Write [.]
// instead of \. and the situation cannot arise.
func TestRuleSQLHasNoBackslash(t *testing.T) {
	for preset, r := range rules {
		if strings.Contains(r.SQL, `\`) {
			t.Errorf("%v: SQL %q contains a backslash; use a POSIX bracket expression such as [.] instead", preset, r.SQL)
		}
	}
}

// TestColumnCheckCombinesClauses pins the one-constraint-per-column rule. GORM
// parses a field's tag into a map keyed by setting name, so a second `check:`
// would overwrite the first and quietly drop a constraint the SQL target emits.
func TestColumnCheckCombinesClauses(t *testing.T) {
	// Two DB-expressible presets on one column must yield exactly one check.
	lower := rules[ormpbv1.Validate_VALIDATE_LOWERCASE]
	trimmed := rules[ormpbv1.Validate_VALIDATE_TRIMMED]
	got := strings.Join([]string{
		strings.ReplaceAll(lower.SQL, "{}", "handle"),
		strings.ReplaceAll(trimmed.SQL, "{}", "handle"),
	}, " AND ")
	want := "handle = lower(handle) AND handle = btrim(handle)"
	if got != want {
		t.Errorf("clause join produced %q, want %q", got, want)
	}
}

// TestChecksSubstituteEveryPlaceholder covers the multi-placeholder rules
// (TRIMMED, LOWERCASE, FINITE), where a single-substitution implementation would
// emit SQL that still contains a literal {}.
func TestChecksSubstituteEveryPlaceholder(t *testing.T) {
	for preset, r := range rules {
		if r.SQL == "" {
			continue
		}
		got := strings.ReplaceAll(r.SQL, "{}", `"col"`)
		if strings.Contains(got, "{}") {
			t.Errorf("%v: placeholder left unsubstituted in %q", preset, got)
		}
	}
}
