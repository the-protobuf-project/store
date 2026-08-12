package wire_test

// manifest_test.go parses the repo's plugin.yaml through protokit's manifest
// package.
//
// The file is hand-written, rarely read back, and consumed by nothing in this
// repo — exactly the shape of file where a typo survives for months. Decoding is
// strict, so `optional_read` for `optional_reads` is an error here rather than a
// declaration that silently says nothing.

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/the-protobuf-project/protokit/manifest"
)

func TestPluginManifest(t *testing.T) {
	// The test runs in plugin/factory/wire; the manifest is at the repo root.
	path := filepath.Join("..", "..", "..", "plugin.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	m, err := manifest.Parse(data)
	if err != nil {
		t.Fatalf("plugin.yaml is not a valid manifest: %v", err)
	}

	if m.Provides != "store" {
		t.Errorf("provides = %q, want %q", m.Provides, "store")
	}
	if !slices.Contains(m.Requires, "protokit") {
		t.Errorf("requires = %v, want it to include protokit", m.Requires)
	}
	// The neutral vocabulary is declared as an annotation dependency like any
	// other, even though it ships from this repository. It is a separate BSR module
	// on a separate tag precisely so a consumer can be running a plugin built
	// against an older one, and a manifest that omitted it would say this plugin
	// reads only its own options — which is the claim entity.v1 exists to falsify.
	if got := m.Annotations["buf.build/the-protobuf-project/entity"]; got != "^1" {
		t.Errorf("entity annotation constraint = %q, want %q", got, "^1")
	}
	// protokit is an engine dependency, not an annotation one — it is declared
	// under requires:, above. It ships no annotation module at all since entity.v1
	// moved out of it, so listing it here would be wrong rather than redundant.
	if _, ok := m.Annotations["buf.build/the-protobuf-project/protokit"]; ok {
		t.Error("annotations lists protokit, which ships no annotation module")
	}
	// Declaring one's own vocabulary as a dependency says nothing; the manifest
	// package rejects the exact-name case, and this covers the versioned spelling.
	if slices.Contains(m.Facets.Reads, "store.v1") {
		t.Error("facets.reads lists store.v1, which this plugin provides")
	}
	if !slices.Contains(m.Facets.OptionalReads, "telemetry.v1") {
		t.Errorf("facets.optional_reads = %v, want it to include telemetry.v1", m.Facets.OptionalReads)
	}

	// Every target this plugin ships writes at least one file, so an empty
	// outputs list would understate the footprint a scheduler has to reason about.
	if len(m.Outputs) == 0 {
		t.Error("outputs is empty; the plugin writes files")
	}
}
