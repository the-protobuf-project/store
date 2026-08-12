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

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/golden"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/source/proto/backend"
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

		// A schema written entirely in the pre-split orm.v1 vocabulary. It builds
		// by default — that is the compatibility promise — and the deprecation is
		// filed under "lint", so a project that wants to force the migration can
		// turn it into a hard error with strict=lint:error.
		{"legacy_vocab", "testdata/strict/legacy_vocab", "lint", "entity.v1-compat"},
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

// TestLegacyVocabularyStructure pins what the strict-mode case above cannot: that
// a proto written in the deprecated vocabulary still produces the *right* schema,
// not merely a successful build.
//
// The distinction matters more than it looks. If entity.CompatReader stopped
// resolving orm.v1 — a dynamicpb change, a descriptor no longer carrying the
// extension declarations, a typo in an extension name — every annotation in the
// fixture would be silently ignored. The build would still succeed, the strict
// case above would still pass by default, and the only symptom would be tables
// coming out with the wrong names, no surrogate key, and no timestamps. Reading
// the values back is the only assertion that fails when the reader stops working.
func TestLegacyVocabularyStructure(t *testing.T) {
	req := golden.BuildRequest(t, "testdata/strict/legacy_vocab")
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen: %v", err)
	}
	ir, err := protokit.Build(p, protokit.Options{Target: "sql"},
		backend.Readers(backend.New(nil, "", false, false, false, false)), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	var widgets *schema.Table
	for _, db := range ir.Databases {
		for _, sc := range db.Schemas {
			for _, tbl := range sc.Tables {
				if tbl.Name == "legacy_widgets" {
					widgets = tbl
				}
			}
		}
	}
	if widgets == nil {
		t.Fatal(`no table named "legacy_widgets": (orm.v1.table).table was not read`)
	}

	cols := map[string]*schema.Column{}
	for _, c := range widgets.Columns {
		cols[c.Name] = c
	}

	// (orm.v1.column).column — the rename reached the neutral name.
	if _, ok := cols["display_label"]; !ok {
		t.Error(`column "display_label" missing: (orm.v1.column).column was not read`)
	}
	// (orm.v1.column).skip — the excluded field produced no column.
	if _, ok := cols["internal"]; ok {
		t.Error(`column "internal" present: (orm.v1.column).skip was not read`)
	}
	// (orm.v1.table).timestamps — the audit columns were synthesized.
	for _, name := range []string{"created_at", "updated_at"} {
		if _, ok := cols[name]; !ok {
			t.Errorf("column %q missing: (orm.v1.table).timestamps was not read", name)
		}
	}
	// (orm.v1.table).id — the ULID surrogate key was synthesized. orm.v1 numbers
	// ID_STRATEGY_ULID differently from entity.v1, so this also pins that the
	// compat reader maps the enum by value name rather than by number.
	id, ok := cols["id"]
	if !ok {
		t.Fatal(`column "id" missing: (orm.v1.table).id was not read`)
	}
	if !id.PrimaryKey {
		t.Error(`column "id" is not the primary key: (orm.v1.table).id was misread`)
	}
	// (orm.v1.table).indexes — the declared composite index survived.
	found := false
	for _, idx := range widgets.Indexes {
		if idx.Name == "idx_widget_label" {
			found = true
		}
	}
	if !found {
		t.Error(`index "idx_widget_label" missing: (orm.v1.table).indexes was not read`)
	}
}
