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

	"github.com/the-protobuf-project/orm/plugin/factory/source/proto/backend"
	"github.com/the-protobuf-project/orm/plugin/factory/wire"
	"github.com/the-protobuf-project/protokit/golden"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/schema"
)

// defaultTargets are the database backends every golden case runs unless it
// ships a "targets" file.
var defaultTargets = []string{"gorm", "prisma", "sql"}

// ormBackend builds the orm Backend for one golden case: it reads any orm.yaml
// the case ships (grouping/telemetry config) and its optional "stores"/
// "converters"/"filters"/"telemetry" markers, and mirrors the binary's opt
// defaults (go_module set so the gorm aggregator is emitted; telemetry off, as
// in the binary, unless the case ships the marker). protokit's harness stays
// generator-neutral — all of this generator-specific knowledge lives here, not
// in RunCase.
func ormBackend(dir string) schema.Backend {
	var cfg *backend.Config
	if path := filepath.Join(dir, "orm.yaml"); fileExists(path) {
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
	validation := fileExists(filepath.Join(dir, "validation"))
	return backend.New(cfg, "example.com/test/gen", stores, telemetry, converters, filters).
		WithValidation(validation).
		WithRepositoryModules(optFile(dir, "gorm_module"), optFile(dir, "graphql_module"))
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
// the protoc-gen-orm binary produces (protokit's generator-neutral default names
// the framework instead).
func TestMain(m *testing.M) {
	header.SetTool("protoc-gen-orm")
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
			golden.RunCase(t, filepath.Join("testdata", "cases", c.Name()), wire.ProtoTargets(), defaultTargets, ormBackend)
		})
	}
}
