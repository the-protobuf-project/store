// Package resources generates the runtime resource descriptors that let a
// generated API be served by any storage backend.
//
// Output layout — one file per database:
//
//	<db>/grpcx/resources.go
//
// # Why this target exists
//
// Every other target in this plugin emits backend-specific code: GORM structs
// bound to gorm.io/gorm, PostgreSQL DDL, a Prisma schema. Each one commits the
// generated API to one storage engine at generation time.
//
// This target emits none. It renders the IR as a slice of
// runtime-go's store.Resource — a descriptor carrying the table, the primary
// key, every column's kind and backend type, and the foreign keys — which the
// runtime reads through protoreflect. Because the descriptor says what a Book
// *is* rather than how one is stored, the same generated output is served by a
// relational driver, an EVM contract, or a Hyperledger chain without the proto
// API or the gRPC adapter changing:
//
//	reg := store.NewRegistry(grpcx.Resources...)
//	svc := adapter.New(orm.New(db), reg) // or evm.New(cfg), fabric.New(), ...
//
// That is also what makes it the seam another protokit plugin builds on: a
// cache, streams, or documentation generator consumes descriptors rather than
// re-deriving table and column layout from protos itself.
//
// # What it deliberately does not emit
//
// Synthesized tables are skipped — outbox tables and many-to-many join tables.
// They are materialized by the generator rather than declared by a proto
// message, so there is no concrete type for store.Resource.New to construct, and
// a descriptor with a nil New would panic in the bridge the first time a driver
// read one. The sql and gorm targets still create them; they are simply not
// addressable through the resource registry. The generated file names the ones
// it dropped rather than leaving the gap silent.
package resources

import (
	"fmt"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/protokit/templates"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
)

// storeModule is the import path of the runtime descriptor contract the
// generated file binds to. It is a dependency of the *generated* code only —
// this plugin never imports it, which is why the root module's go.mod does not
// require runtime-go.
const storeModule = "github.com/the-protobuf-project/runtime-go/database/store"

// pkgName is the Go package the descriptors are generated into. It matches the
// package name runtime-go's adapter documentation uses (grpcx.Resources).
const pkgName = "grpcx"

// Generator implements schema.IRTarget for runtime resource descriptors.
type Generator struct{}

var _ schema.IRTarget = (*Generator)(nil)

// Name returns the target identifier used in buf.gen.yaml opt: [target=resources].
func (g *Generator) Name() string { return "resources" }

// Generate renders from the databases alone, for callers that have no IR. Column
// SQL types then fall back to the neutral FieldType, since the store.v1 overrides
// live in the facets this form does not carry.
func (g *Generator) Generate(p *protogen.Plugin, dbs []*schema.Database) error {
	return g.GenerateIR(p, &schema.IR{Databases: dbs})
}

// GenerateIR writes one grpcx/resources.go per database into the plugin response.
func (g *Generator) GenerateIR(p *protogen.Plugin, ir *protokit.IR) error {
	fx := facets.New(ir)
	idx := newPbIndex(p)
	for _, db := range ir.Databases {
		view, err := databaseView(db, idx, fx)
		if err != nil {
			return fmt.Errorf("resources: %s: %w", db.Name, err)
		}
		// A database whose every table is a synthesized join table has no
		// descriptor to write. Emitting an empty Resources slice would be a
		// working but misleading file, so write nothing at all.
		if len(view.Resources) == 0 {
			continue
		}
		path := fmt.Sprintf("%s/%s/resources.go", db.Name, pkgName)
		f := p.NewGeneratedFile(path, "")
		if err := templates.Render(tmpl, f, "resources.go.tpl", view.data()); err != nil {
			return fmt.Errorf("resources: %s: %w", path, err)
		}
	}
	return nil
}
