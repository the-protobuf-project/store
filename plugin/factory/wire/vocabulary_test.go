package wire_test

// vocabulary_test.go is the proof that splitting orm.v1 into protokit.v1
// (structure) and store.v1 (storage) changed nothing about what this plugin
// generates.
//
// The golden files alone cannot prove it. They are all written in orm.v1, so
// they only show that the *compatibility* path still works — which would stay
// true even if the new vocabularies were wired up wrong, or not wired up at all.
// The claim that matters is the other one: a schema migrated to protokit.v1 +
// store.v1 produces exactly what its orm.v1 original did.
//
// testdata/vocabulary/protokitv1_only holds a line-for-line twin of the bookstore
// example with every annotation moved to its new home. It lives outside
// testdata/cases/ deliberately: TestGolden would otherwise demand a golden tree
// for it, and that tree would be a byte-for-byte copy of the bookstore one —
// duplicating the very thing this test asserts. Rendering both through the same
// targets and comparing byte for byte is the assertion.

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/golden"
)

// TestVocabularyEquivalence renders the orm.v1 bookstore and its migrated twin
// through every default target and requires identical output.
//
// A failure here means the split lost or changed a meaning: an option that moved
// to the wrong vocabulary, a facet the targets no longer read, or a structure
// reader protokit consults in the wrong order.
func TestVocabularyEquivalence(t *testing.T) {
	for _, target := range defaultTargets {
		t.Run(target, func(t *testing.T) {
			legacy := renderCase(t, filepath.Join("testdata", "cases", "bookstore"), target)
			migrated := renderCase(t, filepath.Join("testdata", "vocabulary", "protokitv1_only"), target)

			if len(legacy) == 0 {
				t.Fatalf("%s: the orm.v1 case produced no files", target)
			}
			for _, path := range sortedPaths(legacy, migrated) {
				want, inLegacy := legacy[path]
				got, inMigrated := migrated[path]
				switch {
				case !inMigrated:
					t.Errorf("%s: %s generated from orm.v1 but not from protokit.v1+store.v1", target, path)
				case !inLegacy:
					t.Errorf("%s: %s generated from protokit.v1+store.v1 but not from orm.v1", target, path)
				case want != got:
					t.Errorf("%s: %s differs between vocabularies:\n%s", target, path, firstDiff(want, got))
				}
			}
		})
	}
}

// renderCase compiles a case directory and returns the target's output, keyed by
// generated path. It drives the same plugin the binary assembles, so a target
// that reads the wrong facet fails here rather than passing on a stub.
func renderCase(t *testing.T, dir, target string) map[string]string {
	t.Helper()
	req := golden.BuildRequest(t, dir)
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen: %v", err)
	}
	if err := protokit.RunPlugin(p, protokit.Options{Target: target}, ormCasePlugin(dir)); err != nil {
		t.Fatalf("%s: %v", target, err)
	}
	resp := p.Response()
	if resp.Error != nil {
		t.Fatalf("%s: %s", target, *resp.Error)
	}
	out := map[string]string{}
	for _, f := range resp.File {
		out[f.GetName()] = f.GetContent()
	}
	return out
}

// sortedPaths returns the union of both maps' keys, sorted, so failures are
// reported in a stable order.
func sortedPaths(a, b map[string]string) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// firstDiff renders the first differing line of two files with a little context,
// which is all that is needed to place a vocabulary mismatch.
func firstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		w, g := lineAt(wl, i), lineAt(gl, i)
		if w != g {
			return "  line " + strconv.Itoa(i+1) + "\n  orm.v1:   " + w + "\n  store.v1: " + g
		}
	}
	return "  (files differ only in trailing content)"
}

// lineAt is s[i] with a readable placeholder past the end, so a file that is a
// prefix of the other reports where it stopped rather than panicking.
func lineAt(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "(no such line)"
}
