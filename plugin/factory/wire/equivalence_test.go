package wire_test

// equivalence_test.go pins the promise MIGRATING.md makes: migrating a proto off
// the deprecated vocabulary changes what you import and what prefix you write, and
// changes nothing about what you get.
//
// That promise is the whole reason the migration is safe to do incrementally, and
// it is not implied by any other test here. TestLegacyVocabularyStructure checks
// that the compat reader delivers the right *values*; it would keep passing if the
// two vocabularies produced subtly different output — a different column order, a
// nullability that only entity.v1 infers, an index name assembled from a different
// source. Those are precisely the differences a user would discover after
// migrating, in a schema diff against a live database.

import (
	"testing"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/golden"
)

// TestLegacyVocabularyEquivalence requires the orm.v1 fixture and its entity.v1
// twin to generate byte-identical output.
//
// It runs every database target rather than one: the vocabularies are read once
// into a shared IR, but each target renders from it independently, so a divergence
// that never reaches the SQL DDL can still reach a GORM struct tag.
func TestLegacyVocabularyEquivalence(t *testing.T) {
	for _, target := range []string{"sql", "gorm", "prisma"} {
		t.Run(target, func(t *testing.T) {
			legacy := generateFiles(t, "testdata/strict/legacy_vocab", target)
			migrated := generateFiles(t, "testdata/strict/legacy_vocab_twin", target)

			if len(legacy) == 0 {
				t.Fatal("the orm.v1 fixture generated no files; the comparison would pass vacuously")
			}

			for name, want := range legacy {
				got, ok := migrated[name]
				if !ok {
					t.Errorf("%s: generated from orm.v1 but not from entity.v1", name)
					continue
				}
				if got != want {
					t.Errorf("%s: entity.v1 output differs from orm.v1\n--- orm.v1 ---\n%s\n--- entity.v1 ---\n%s", name, want, got)
				}
			}
			for name := range migrated {
				if _, ok := legacy[name]; !ok {
					t.Errorf("%s: generated from entity.v1 but not from orm.v1", name)
				}
			}
		})
	}
}

// generateFiles runs target over the case in dir and returns the generated files
// keyed by path.
func generateFiles(t *testing.T, dir, target string) map[string]string {
	t.Helper()
	req := golden.BuildRequest(t, dir)
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen: %v", err)
	}
	if err := protokit.RunPlugin(p, protokit.Options{Target: target}, ormPlugin()); err != nil {
		t.Fatalf("generate %s: %v", target, err)
	}
	out := map[string]string{}
	for _, f := range p.Response().File {
		out[f.GetName()] = f.GetContent()
	}
	return out
}
