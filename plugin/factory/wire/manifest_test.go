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
	// The floor is load bearing: facets, the neutral vocabulary, and IRTarget all
	// arrived in protokit v1.2.0, and this plugin cannot build against less.
	if got := m.Annotations["buf.build/the-protobuf-project/protokit"]; got != ">=1.2.0" {
		t.Errorf("protokit annotation constraint = %q, want %q", got, ">=1.2.0")
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
