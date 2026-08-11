package golang_test

// Golden test for the GraphQL-client renderer. Each directory under
// testdata/ is a case with a schema.json (a GraphQL introspection payload); the
// renderer's full output is compared byte-for-byte against <case>/golden/.
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
	"testing"

	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"github.com/the-protobuf-project/protokit/graphql/introspect"
	"github.com/the-protobuf-project/protokit/graphql/ir"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/store/plugin/factory/target/graphql/golang"
)

var update = flag.Bool("update", false, "rewrite golden files from current output")

// TestMain stamps the store tool name into generated banners so goldens match
// what the protoc-gen-store binary produces (every target shares the exact same
// header format and tool stamp).
func TestMain(m *testing.M) {
	header.SetTool("protoc-gen-store")
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
			raw, err := os.ReadFile(filepath.Join(dir, "schema.graphql"))
			if err != nil {
				t.Fatal(err)
			}
			schema, err := introspect.ParseSDL("schema.graphql", string(raw))
			if err != nil {
				t.Fatalf("parse SDL: %v", err)
			}
			out := t.TempDir()
			d := dialect.Default()
			err = golang.Generate(golang.Options{
				Schema:        ir.Build(schema, d),
				OutDir:        out,
				Package:       testPackage,
				GoModule:      testGoModule,
				RuntimeModule: testRuntimeModule,
				MaxDepth:      1,
				Dialect:       d,
			})
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			goldenDir := filepath.Join(dir, "golden")
			got := collect(t, filepath.Join(out, testPackage))
			if *update {
				writeGolden(t, goldenDir, got)
				return
			}
			want := collect(t, goldenDir)
			compare(t, want, got)
		})
	}
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
