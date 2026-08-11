// Package wire is the factory's composition root: it assembles the registered
// sources and targets into one Registry. It lives outside the factory package
// because it imports the concrete targets (which import factory), which the
// factory package importing them back would make a cycle. A single Registry
// constructor keeps plugin dispatch, store.yaml validation, and the tests all
// agreeing on the exact source/target set — add a new source or target here once.
//
// The graphql source and the graphql target are both package `graphql` (at
// different paths). To keep every import bare — no aliases — the two are added and
// constructed in separate files (graphqlsource.go, graphqltarget.go); this file
// touches neither graphql package directly.
package wire

import (
	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/factory"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/coreir"
	"github.com/the-protobuf-project/store/plugin/factory/source/proto"
	"github.com/the-protobuf-project/store/plugin/factory/target/database"
	"github.com/the-protobuf-project/store/plugin/factory/target/gorm"
	"github.com/the-protobuf-project/store/plugin/factory/target/prisma"
	"github.com/the-protobuf-project/store/plugin/factory/target/repository"
	"github.com/the-protobuf-project/store/plugin/factory/target/sql"
)

// ProtoTargets returns the proto-source database targets — the ones that satisfy
// protokit's schema.Target (rendering from proto-derived databases). It is only the
// proto subset, not the full target set: the graphql target renders from a GraphQL
// schema and implements factory.Target[*coreir.Model] instead, so it lives in Registry, not here.
// This subset exists because the golden test harness drives schema.Target directly.
func ProtoTargets() map[string]schema.Target {
	return map[string]schema.Target{
		"gorm":       &gorm.Generator{},
		"sql":        &sql.Generator{},
		"prisma":     &prisma.Generator{},
		"repository": &repository.Generator{},
	}
}

// Plugin assembles what this binary contributes to one protokit run: the proto
// targets it ships, the readers that bring its annotation package into the IR as
// facets, and the naming policy it resolved from its own config.
//
// It exists so plugin dispatch, the golden harness, and the agreement tests all
// construct the same run — a test that built its readers by hand would be testing
// a plugin the binary never assembles.
func Plugin(readers []protokit.FacetReader, layout protokit.LayoutResolver) protokit.Plugin {
	return protokit.Plugin{Registry: ProtoTargets(), Readers: readers, Layout: layout}
}

// Registry builds the full factory registry: the proto and graphql sources plus
// every target. protoOpts, readers, and layout configure the proto source; the
// graphql source and target are added with zero config, which is enough to
// validate and list them (a graphql run configures its own instances from
// store.yaml, via NewGraphQLSource/NewGraphQLTarget). This one constructor is used
// by plugin dispatch, config validation, and the tests.
func Registry(protoOpts protokit.Options, readers []protokit.FacetReader, layout protokit.LayoutResolver) *factory.Registry[*coreir.Model] {
	reg := factory.NewRegistry[*coreir.Model]()
	reg.AddSource(proto.New(protoOpts, readers, layout))
	addGraphQLSource(reg)
	for _, t := range ProtoTargets() {
		reg.AddTarget(database.Wrap(t))
	}
	addGraphQLTarget(reg)
	return reg
}
