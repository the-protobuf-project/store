package gorm

// filter_view.go prepares the template views for the filters feature: the
// per-schema filters.go spec file and the once-per-tree filterx package
// (backend-neutral core plus the chainable Gorm and Hasura engines, and — with
// the telemetry opt — the opentelementry Observer adapter). The planning logic
// lives in filterspec.go; the templates are presentation only.

import (
	"strings"

	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
)

// filterxPkg is the package name and output directory of the shared filter
// engine package at <go_module>/filterx (core + both engines, one package).
const filterxPkg = "filterx"

// graphqlRuntimeModule is the import path of the shared GraphQL predicate DSL
// the hasura engine builds on (Predicate, typed fields, OrderTerm, and the
// generic QueryHandler the list engine drives).
const graphqlRuntimeModule = "github.com/the-protobuf-project/runtime-go/network/graphql"

// filtersView assembles the data for one schema's filters.go: one Spec var per
// table. Returns nil when no table in the schema yields a spec.
func filtersView(db *schema.Database, s *schema.Schema, pkg string, fx facets.Set) map[string]any {
	var tables []filterTableView
	for _, t := range s.Tables {
		if v, ok := buildFilterTable(s, t, fx); ok {
			tables = append(tables, v)
		}
	}
	if len(tables) == 0 {
		return nil
	}
	return map[string]any{
		"Header": header.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Source:        strings.Join(s.SourceProtos(), ", "),
			Database:      db.Name,
			Schema:        s.Name,
			Notes:         []string{"AIP-160 filter / AIP-132 order_by specs — data for the shared filterx engines."},
		}),
		"Package":       pkg,
		"FilterxImport": dbGoModule(db) + "/" + filterxPkg,
		"Tables":        tables,
	}
}

// filterxView is the shared view for every once-per-tree filterx template
// (core, gorm engine, hasura engine, opentelementry adapter — one package).
func filterxView(db *schema.Database) map[string]any {
	return map[string]any{
		"Header": header.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Database:      db.Name,
			SchemaLabel:   "package",
			Schema:        filterxPkg,
			Notes:         []string{"Shared AIP-160 filter / AIP-132 order_by / paginated-list engines driven by the generated per-schema specs."},
		}),
		"GraphQLImport":        graphqlRuntimeModule,
		"OpentelementryImport": opentelementryModule,
	}
}
