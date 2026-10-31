// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package rust generates a Rust time-series client for the tpp-database runtime
// (github.com/the-protobuf-project/runtime-rs, crate tpp-database): models and
// typed stores over InfluxDB 3 and Arrow IPC captures, laid out as the gorm
// target lays out Go.
//
// Only messages declaring (store.v1.table).time_series are rendered. Both
// runtime backends store points, not keyed rows, so a relational message has
// nothing to become here; it stays with the gorm, prisma and sql targets, and
// a mixed package renders its measurements alone.
//
// How the output divides storage:
//
//   - a proto database (entity.v1.datasource) is one InfluxDB database, and
//     one capture directory under an Arrow root; <db>/mod.rs selects it;
//   - a schema is a Rust module only, since InfluxDB has no schemas, so table
//     names must be unique across a database's schemas;
//   - a message is one table, written and queried through its store.
//
// Output layout, beside gorm's:
//
//	Cargo.toml, lib.rs                       one crate (gorm: go_module)
//	seriesx/mod.rs                           shared runtime (gorm: gormx)
//	<db>/mod.rs                              database selection + registry (gorm: migrate.go)
//	<db>/README.md
//	<db>/<schema>/mod.rs
//	<db>/<schema>/models.rs                  structs, enums, descriptors (gorm: models.go)
//	<db>/<schema>/<model>_store.rs           typed store + query (gorm: <model>_store.go)
package influxdb

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/protokit/templates"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
	"github.com/the-protobuf-project/store/plugin/factory/provenance"
)

// RuntimeCrate is the crate the generated code depends on, RuntimeGit where
// Cargo fetches it from, and RuntimeBranch the branch it tracks: always main,
// so a regenerated crate builds against the runtime as it stands. A consumer
// building against a local checkout redirects it with [patch."<RuntimeGit>"]
// rather than editing generated output.
const (
	RuntimeCrate  = "tpp-database"
	RuntimeGit    = "https://github.com/the-protobuf-project/runtime-rs"
	RuntimeBranch = "main"
)

// Generator implements schema.IRTarget for the Rust time-series client.
type Generator struct{}

var _ schema.IRTarget = (*Generator)(nil)

// Languages lists the languages this target emits. A new one is a template set
// beside templates/rust.
func (g *Generator) Languages() []string { return []string{"rust"} }

// Name returns the target identifier used in buf.gen.yaml opt: [lang=rust, target=influxdb].
func (g *Generator) Name() string { return "influxdb" }

// Generate renders from the databases alone. With no facets nothing declares a
// time series, so it writes nothing; GenerateIR is the real entry point.
func (g *Generator) Generate(p *protogen.Plugin, dbs []*schema.Database) error {
	return g.GenerateIR(p, &schema.IR{Databases: dbs})
}

// GenerateIR writes the crate: its manifest, root and shared runtime once, then
// every database that declares a measurement.
func (g *Generator) GenerateIR(p *protogen.Plugin, ir *protokit.IR) error {
	tv, err := treeOf(ir.Databases, facets.New(ir))
	if err != nil || tv == nil {
		return err
	}
	plugin, protoc := ir.Databases[0].PluginVersion, ir.Databases[0].ProtocVersion
	banner := func(prefix string, in header.Info, note string) string {
		in.PluginVersion, in.ProtocVersion, in.Notes = plugin, protoc, []string{note}
		return provenance.Render(prefix, in, RuntimeCrate)
	}

	tv.TOMLHeader = banner("#", header.Info{}, "The crate of every generated time-series store.")
	tv.Header = banner("//", header.Info{}, "The crate root: one module per database.")
	if err := render(p, "Cargo.toml", "Cargo.toml.tpl", tv); err != nil {
		return err
	}
	if err := render(p, "lib.rs", "lib.rs.tpl", tv); err != nil {
		return err
	}
	tv.Header = banner("//", header.Info{}, "Shared runtime of the generated stores, as gormx is for gorm.")
	if err := render(p, runtimeModule+"/mod.rs", "seriesx.rs.tpl", tv); err != nil {
		return err
	}

	for _, d := range tv.Databases {
		var schemas []string
		for _, m := range d.Schemas {
			schemas = append(schemas, m.Schema)
		}
		d.Header = banner("//", header.Info{Database: d.Name, SchemaLabel: "schemas", Schema: strings.Join(schemas, ", ")},
			"Selects the "+d.Name+" database on InfluxDB 3 or Arrow captures.")
		if err := render(p, d.Name+"/mod.rs", "database.rs.tpl", d); err != nil {
			return err
		}
		if err := render(p, d.Name+"/README.md", "README.md.tpl", d); err != nil {
			return err
		}
		for _, m := range d.Schemas {
			dir := d.Name + "/" + m.Module + "/"
			info := header.Info{Database: d.Name, Schema: m.Schema, Source: m.Source}
			m.Header = banner("//", info, "The "+m.Schema+" schema's measurements and stores.")
			if err := render(p, dir+"mod.rs", "schema.rs.tpl", m); err != nil {
				return err
			}
			m.Header = banner("//", info, "Models of the "+m.Schema+" schema.")
			if err := render(p, dir+"models.rs", "models.rs.tpl", m); err != nil {
				return err
			}
			for _, md := range m.Models {
				sv := storeView{
					Header: banner("//", info, "Store of the "+md.Table+" table."),
					Lib:    d.Lib,
					Module: d.Module + "::" + m.Module,
					Model:  md,
				}
				if err := render(p, dir+md.StoreFile+".rs", "store.rs.tpl", sv); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// storeView is the template data of one <model>_store.rs.
type storeView struct {
	Header string
	Lib    string
	Module string // the schema module's path from the crate root
	Model  *modelView
}

func render(p *protogen.Plugin, path, tpl string, data any) error {
	if err := templates.Render(tmpl, p.NewGeneratedFile(path, ""), tpl, data); err != nil {
		return fmt.Errorf("influxdb: %s: %w", path, err)
	}
	return nil
}
