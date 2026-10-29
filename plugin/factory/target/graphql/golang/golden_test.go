// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package golang_test

// Golden test for the GraphQL-client renderer. Each directory under
// testdata/ is a case with a schema.graphql (GraphQL SDL); the renderer's full
// output is compared byte-for-byte against <case>/golden/. A case with a
// prefixes file (root_field_prefix=..., type_name_prefix=... lines) is scoped
// to that DDN subgraph first, as the graphql source does.
// This is the regression fence for the dialect refactor: capture the golden
// from the pre-refactor renderer, then require the hasura dialect to reproduce
// it exactly.
//
// Regenerate goldens after an intentional output change:
//
//	go test ./plugin/factory/target/graphql/golang -run TestGenerateGolden -update

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"github.com/the-protobuf-project/protokit/graphql/introspect"
	"github.com/the-protobuf-project/protokit/graphql/ir"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/store/plugin/factory/provenance"
	"github.com/the-protobuf-project/store/plugin/factory/subgraph"
	"github.com/the-protobuf-project/store/plugin/factory/target/graphql/golang"
)

var update = flag.Bool("update", false, "rewrite golden files from current output")

// TestMain stamps the store tool name into generated banners so goldens match
// what the protoc-gen-store binary produces (every target shares the exact same
// header format and tool stamp).
func TestMain(m *testing.M) {
	header.SetTool("protoc-gen-store")
	// The engine version is read from build info, which answers differently
	// depending on how protokit was resolved and which toolchain built the test
	// binary. Pin it so the goldens compare output, not the build environment.
	provenance.SetEngineVersion(provenance.Unknown)
	os.Exit(m.Run())
}

// fixed generation options, so goldens are stable across runs.
const (
	testGoModule      = "example.com/gen"
	testPackage       = "genql"
	testRuntimeModule = "github.com/the-protobuf-project/runtime-go/network/runtime"
)

func TestGenerateGolden(t *testing.T) {
	cases, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if !c.IsDir() {
			continue
		}
		name := c.Name()
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join("testdata", name)
			goldenDir := filepath.Join(dir, "golden")
			got := generateCase(t, dir)
			if *update {
				writeGolden(t, goldenDir, got)
				return
			}
			want := collect(t, goldenDir)
			compare(t, want, got)
		})
	}
}

// generateCase renders case dir's schema.graphql, scoped by its prefixes file
// when it has one, and returns the generated files.
func generateCase(t *testing.T, dir string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "schema.graphql"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := introspect.ParseSDL("schema.graphql", string(raw))
	if err != nil {
		t.Fatalf("parse SDL: %v", err)
	}
	root, typ := casePrefixes(t, dir)
	scoped, err := subgraph.Scope(schema, root, typ)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	out := t.TempDir()
	d := dialect.Default()
	err = golang.Generate(golang.Options{
		Schema:        ir.Build(scoped.Bare, d),
		OutDir:        out,
		Package:       testPackage,
		GoModule:      testGoModule,
		RuntimeModule: testRuntimeModule,
		MaxDepth:      1,
		Dialect:       d,
		Names:         scoped.Names,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return collect(t, filepath.Join(out, testPackage))
}

// casePrefixes reads a case's prefixes file; both empty when it has none.
func casePrefixes(t *testing.T, dir string) (root, typ string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "prefixes"))
	if os.IsNotExist(err) {
		return "", ""
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "root_field_prefix":
			root = value
		case "type_name_prefix":
			typ = value
		}
	}
	return root, typ
}

// The prefixed case is the hasura case twice over, as two prefixed subgraphs
// of one engine. Scoped to one of them, the client must be the hasura client:
// the same files and Go identifiers, with only the GraphQL wire names (root
// fields and variable types, in strings and the comments quoting them)
// carrying the prefixes. Removing them must give the hasura golden exactly.
func TestPrefixedSubgraphMatchesUnprefixed(t *testing.T) {
	got := generateCase(t, filepath.Join("testdata", "prefixed"))
	want := collect(t, filepath.Join("testdata", "hasura", "golden"))
	wire := 0
	for rel, content := range got {
		wire += strings.Count(content, `"aperio_`) + strings.Count(content, "Aperio_")
		if strings.Contains(content, "other_") || strings.Contains(content, "Other_") {
			t.Errorf("%s: the other subgraph's names leaked into the client", rel)
		}
		got[rel] = strings.NewReplacer(`"aperio_`, `"`, "Aperio_", "").Replace(content)
	}
	if wire == 0 {
		t.Fatal("no wire name carries a prefix")
	}
	compare(t, want, got)
}

// collect walks root and returns a map of relative path -> file contents.
func collect(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[rel] = string(b)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return files
}

func writeGolden(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func compare(t *testing.T, want, got map[string]string) {
	t.Helper()
	for rel, w := range want {
		g, ok := got[rel]
		if !ok {
			t.Errorf("missing generated file %s", rel)
			continue
		}
		if g != w {
			t.Errorf("file %s differs from golden", rel)
		}
	}
	for rel := range got {
		if _, ok := want[rel]; !ok {
			t.Errorf("unexpected generated file %s (not in golden)", rel)
		}
	}
}
