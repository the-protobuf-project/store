package wire_test

// Golden-file tests for the database backends. Every directory under
// testdata/cases/ is one case: its .proto files are compiled in-process (via the
// protokit golden harness) and each database target's output is compared
// byte-for-byte against <case>/golden/<target>/. RunCase skips any target
// this module's registry doesn't ship.
//
// Regenerate goldens after an intentional output change:
//
//	go test ./plugin/factory/wire -run TestGolden -update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/golden"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/store/plugin/factory/source/proto/backend"
	"github.com/the-protobuf-project/store/plugin/factory/wire"
)

// defaultTargets are the database backends every golden case runs unless it
// ships a "targets" file.
var defaultTargets = []string{"gorm", "prisma", "sql"}

// ormPlugin builds the protokit.Plugin for one golden case: it reads any store.yaml
// the case ships (grouping/telemetry config) and its optional "stores"/
// "converters"/"filters"/"telemetry" markers, and mirrors the binary's opt
// defaults (go_module set so the gorm aggregator is emitted; telemetry off, as
// in the binary, unless the case ships the marker). protokit's harness stays
// generator-neutral — all of this generator-specific knowledge lives here, not
// in the harness.
//
// ormPlugin is the plugin for a run with no fixture config, which is what the
// non-golden tests (targets, strict) want: the orm.v1 reader, no layout policy.
// They still need the reader — their fixtures declare provider and indexes
// through orm.v1, and a run without it would not see them.
func ormPlugin() protokit.Plugin {
	return wire.Plugin([]protokit.FacetReader{backend.New(nil, "", false, false, false, false), backend.NewCompat()}, nil)
}

// ormCasePlugin is ormPlugin for a golden case directory.
func ormCasePlugin(dir string) protokit.Plugin {
	var cfg *backend.Config
	if path := filepath.Join(dir, "store.yaml"); fileExists(path) {
		c, err := backend.LoadConfig(path)
		if err != nil {
			panic(err) // a malformed fixture config is a test-authoring bug
		}
		cfg = c
	}
	stores := fileExists(filepath.Join(dir, "stores"))
	converters := fileExists(filepath.Join(dir, "converters"))
	filters := fileExists(filepath.Join(dir, "filters"))
	telemetry := fileExists(filepath.Join(dir, "telemetry"))
	reader := backend.New(cfg, "example.com/test/gen", stores, telemetry, converters, filters).
		WithRepositoryModules(optFile(dir, "gorm_module"), optFile(dir, "graphql_module"))
	return wire.Plugin([]protokit.FacetReader{reader, backend.NewCompat()}, backend.NewLayout(cfg))
}

// optFile reads a valued marker file (e.g. gorm_module holding a module path),
// returning "" when the case doesn't ship it.
func optFile(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestMain stamps the orm tool name into generated banners so goldens match what
// the protoc-gen-store binary produces (protokit's generator-neutral default names
// the framework instead).
func TestMain(m *testing.M) {
	header.SetTool("protoc-gen-store")
	os.Exit(m.Run())
}

func TestGolden(t *testing.T) {
	cases, err := os.ReadDir("testdata/cases")
	if err != nil {
		t.Fatalf("read cases: %v", err)
	}
	for _, c := range cases {
		if !c.IsDir() {
			continue
		}
		t.Run(c.Name(), func(t *testing.T) {
			golden.RunPluginCase(t, filepath.Join("testdata", "cases", c.Name()), defaultTargets, ormCasePlugin)
		})
	}
}
