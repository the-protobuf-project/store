// Package gorm generates production-ready Go structs with GORM struct tags.
//
// Output layout follows Go package conventions — one directory per schema,
// package name matching its directory (underscores stripped):
//
//	<db>/<schemapkg>/models.go    e.g. bookstore_db/bookstorev1/models.go
//
// Nullable fields are pointer types (*string, *int32, …). Proto enums become
// string-typed Go enums with one const per value. Imports are conditional.
package gorm

import (
	"fmt"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/docs"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
	"github.com/the-protobuf-project/store/plugin/factory/target/types"
)

// Generator implements schema.IRTarget for GORM Go struct output.
type Generator struct{}

var _ schema.IRTarget = (*Generator)(nil)

// Name returns the target identifier used in buf.gen.yaml opt: [target=gorm].
func (g *Generator) Name() string { return "gorm" }

// gormxPkg is the package name and output directory of the shared runtime the
// generated stores import; it lives at <go_module>/gormx.
const gormxPkg = "gormx"

// Generate renders from the databases alone, for callers that have no IR. Column
// types then fall back to the neutral FieldType and every field's query surface to
// its type-derived default, since the orm.v1 overrides live in the facets this
// form does not carry.
func (g *Generator) Generate(p *protogen.Plugin, dbs []*schema.Database) error {
	return g.GenerateIR(p, &schema.IR{Databases: dbs})
}

// GenerateIR writes one Go package per schema into the plugin response.
func (g *Generator) GenerateIR(p *protogen.Plugin, ir *protokit.IR) error {
	fx := facets.New(ir)
	typeOf := func(c *schema.Column) string { return types.SQLForColumn(c, fx.Column(c)) }
	dbs := ir.Databases
	gormxEmitted := false     // the shared runtime is emitted once for the whole tree
	filterxEmitted := false   // likewise the shared filter engine packages
	telemetryEmitted := false // likewise the SDK adapter package
	var pbIdx *pbIndex        // built lazily: only the converters emitter needs it
	for _, db := range dbs {
		if types.Provider(db.Provider) != types.Postgres {
			return fmt.Errorf("gorm: database %q uses provider %q — the gorm target only supports postgres", db.Name, db.Provider)
		}
		// The stores share one gormx runtime package, imported by its full path, so
		// store generation now needs the go_module opt just like the aggregator.
		if dbStores(db) && dbGoModule(db) == "" {
			return fmt.Errorf("gorm: database %q has the stores opt set but no go_module opt; "+
				"the generated stores import the shared %q runtime package by its import path, "+
				"so set go_module to the import path of the gorm output directory", db.Name, gormxPkg)
		}
		if dbFilters(db) && dbGoModule(db) == "" {
			return fmt.Errorf("gorm: database %q has the filters opt set but no go_module opt; "+
				"the generated specs import the shared %q engine package by its import path, "+
				"so set go_module to the import path of the gorm output directory", db.Name, filterxPkg)
		}
		if dbTelemetry(db) && dbGoModule(db) == "" {
			return fmt.Errorf("gorm: database %q has the telemetry opt set but no go_module opt; "+
				"the instrumented output imports the shared %q adapter package by its import path, "+
				"so set go_module to the import path of the gorm output directory", db.Name, telemetryPkg)
		}
		for _, s := range db.Schemas {
			pkg := naming.GoPackage(s.Name)
			f := p.NewGeneratedFile(fmt.Sprintf("%s/%s/models.go", db.Name, pkg), "")
			if err := renderGo(f, "models.go.tpl", packageView(db, s, pkg, typeOf)); err != nil {
				return fmt.Errorf("gorm: %s/%s: %w", db.Name, pkg, err)
			}
			// Opt-in: proto↔model converters, one protobuf.go per schema package.
			// Skipped when no table in the schema maps back to a proto message.
			if dbConverters(db) && len(s.Tables) > 0 {
				if pbIdx == nil {
					pbIdx = newPbIndex(p)
				}
				view, err := convertView(pbIdx, db, s, pkg, typeOf)
				if err != nil {
					return fmt.Errorf("gorm: %s/%s/protobuf.go: %w", db.Name, pkg, err)
				}
				if view != nil {
					cf := p.NewGeneratedFile(fmt.Sprintf("%s/%s/protobuf.go", db.Name, pkg), "")
					if err := renderGo(cf, "protobuf.go.tpl", view); err != nil {
						return fmt.Errorf("gorm: %s/%s/protobuf.go: %w", db.Name, pkg, err)
					}
				}
			}
			// Opt-in: AIP-160 filter / AIP-132 order_by specs per schema, plus the
			// shared filterx package once for the whole tree (the backend-neutral
			// core with the chainable Gorm and Hasura engines, and — with the
			// telemetry opt — an opentelementry Observer adapter).
			if dbFilters(db) && len(s.Tables) > 0 {
				if view := filtersView(db, s, pkg, fx); view != nil {
					ff := p.NewGeneratedFile(fmt.Sprintf("%s/%s/filters.go", db.Name, pkg), "")
					if err := renderGo(ff, "filters.go.tpl", view); err != nil {
						return fmt.Errorf("gorm: %s/%s/filters.go: %w", db.Name, pkg, err)
					}
					if !filterxEmitted {
						fv := filterxView(db)
						for path, tpl := range map[string]string{
							filterxPkg + "/filterx.go": "filterx.go.tpl",
							filterxPkg + "/gorm.go":    "filterx_gorm.go.tpl",
							filterxPkg + "/hasura.go":  "filterx_hasura.go.tpl",
						} {
							xf := p.NewGeneratedFile(path, "")
							if err := renderGo(xf, tpl, fv); err != nil {
								return fmt.Errorf("gorm: %s: %w", path, err)
							}
						}
						if dbTelemetry(db) {
							pf := p.NewGeneratedFile(filterxPkg+"/opentelementry.go", "")
							if err := renderGo(pf, "filterx_opentelementry.go.tpl", fv); err != nil {
								return fmt.Errorf("gorm: %s/opentelementry.go: %w", filterxPkg, err)
							}
						}
						filterxEmitted = true
					}
				}
			}
			// Opt-in: a typed CRUD store per resource, one file per model, sharing
			// the models package. Skip empty schemas so the shared runtime is only
			// emitted once a store actually uses it.
			if dbStores(db) && len(s.Tables) > 0 {
				if !gormxEmitted {
					gf := p.NewGeneratedFile(fmt.Sprintf("%s/%s.go", gormxPkg, gormxPkg), "")
					if err := renderGo(gf, "gormx.go.tpl", gormxView(db)); err != nil {
						return fmt.Errorf("gorm: %s/%s.go: %w", gormxPkg, gormxPkg, err)
					}
					gormxEmitted = true
				}
				for _, t := range s.Tables {
					sf := p.NewGeneratedFile(fmt.Sprintf("%s/%s/%s", db.Name, pkg, storeFileName(t)), "")
					if err := renderGo(sf, "store_model.go.tpl", storeModelView(db, s, pkg, t, typeOf)); err != nil {
						return fmt.Errorf("gorm: %s/%s/%s: %w", db.Name, pkg, storeFileName(t), err)
					}
				}
			}
		}
		// The SDK adapter package, once per tree: the stores' gormx.Telemetry
		// implementation and the SQL-level gorm plugin Registry.Instrument
		// installs. The only generated code importing the opentelementry SDK.
		if dbTelemetry(db) && !telemetryEmitted {
			tf := p.NewGeneratedFile(telemetryPkg+"/"+telemetryPkg+".go", "")
			if err := renderGo(tf, "telemetry.go.tpl", telemetryPkgView(db)); err != nil {
				return fmt.Errorf("gorm: %s/%s.go: %w", telemetryPkg, telemetryPkg, err)
			}
			telemetryEmitted = true
		}
		// The migration aggregator imports each per-schema package by its full Go
		// import path, so it can only be generated when go_module gives us the
		// output tree's base import path.
		if dbGoModule(db) != "" {
			mf := p.NewGeneratedFile(fmt.Sprintf("%s/migrate.go", db.Name), "")
			if err := renderGo(mf, "migrate.go.tpl", aggregateView(db)); err != nil {
				return fmt.Errorf("gorm: %s/migrate.go: %w", db.Name, err)
			}
		}
		if err := writeReadme(p, db, typeOf); err != nil {
			return err
		}
	}
	return nil
}

// writeReadme documents the generated package tree: an ER diagram and per-model
// reference under the bare, schema-local names the Go packages use.
func writeReadme(p *protogen.Plugin, db *schema.Database, typeOf types.TypeOf) error {
	rf := p.NewGeneratedFile(db.Name+"/README.md", "")
	outputs := []string{
		"`<schema>/models.go` — one Go package per schema, one struct per table.",
		"`migrate.go` — a factory `Registry` (with a preloaded `Default`) that migrates every model in one call; emitted when the `go_module` opt is set. Call `Default.EnsureSchemas(db)` before `Default.Migrate(db)` so the schema-qualified tables have their Postgres schemas.",
		"Nullable columns are pointer types; proto enums become string-typed Go enums.",
		"Attach in main: `Default.EnsureSchemas(db)` then `Default.Migrate(db)`, or wire the structs into a `*gorm.DB` and run AutoMigrate yourself.",
	}
	if dbStores(db) {
		outputs = append(outputs,
			"`<schema>/<model>_store.go` — a typed CRUD store per resource (Create, GetByID, List, Count, Update, DeleteByID, plus GetBy/ListBy finders for unique and foreign-key columns); emitted when the `stores` opt is set (which also requires `go_module`). Requires `gorm.io/gorm`.",
			"`gormx/gormx.go` — the shared runtime every store imports: `ListOptions`, the generic `Store[M]` interface every store satisfies, a `GenericStore[M]` engine that runs CRUD for any model with no per-entity code, and `EnsureSchemas`. Lets one generic engine drive every entity.",
		)
	}
	if dbTelemetry(db) {
		outputs = append(outputs,
			"`telemetry/telemetry.go` — the first-party opentelementry adapter: `New(o)` wraps an SDK handle as the `gormx.Telemetry` the stores observe through (`WithTelemetry(telemetry.New(o))`), and `Plugin(o)` is the SQL-level gorm plugin. Emitted with the `telemetry` opt; requires `github.com/the-protobuf-project/opentelementry/opentelementry-go`.",
			"`Registry.Instrument(db, o)` in `migrate.go` — installs the generated telemetry gorm plugin so every query emits a span (and metric) through the SDK handle.",
		)
	}
	md := docs.Render(db, docs.Meta{
		Title:   "GORM models",
		Tagline: "Go structs with GORM struct tags — one package per schema.",
		Outputs: outputs,
		Naming:  docs.Local(db),
		TypeOf:  typeOf,
	})
	if _, err := rf.Write([]byte(md)); err != nil {
		return fmt.Errorf("gorm: %s/README.md: %w", db.Name, err)
	}
	return nil
}
