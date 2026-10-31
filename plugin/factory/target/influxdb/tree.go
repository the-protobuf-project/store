// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package influxdb

// tree.go lays the output out as the gorm target does: one crate for the
// whole tree (gorm's go_module), a shared runtime beside the databases
// (seriesx, gorm's gormx), and per database a module that selects it plus one
// module per schema holding models.rs and a <model>_store.rs per measurement.

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
)

// runtimeModule is the shared runtime's module, emitted once per tree.
const runtimeModule = "seriesx"

// defaultCrate names the crate when the crate opt is unset.
const defaultCrate = "generated"

// treeView is the template data of Cargo.toml, lib.rs and seriesx.
type treeView struct {
	Header       string
	TOMLHeader   string
	Crate        string // Cargo package name
	Lib          string // the crate as Rust code names it
	RuntimeCrate string
	RuntimeGit   string
	RuntimeRef   string
	Databases    []*dbView
}

// Mods lists lib.rs's module declarations as rustfmt orders them.
func (t *treeView) Mods() []string {
	names := []string{runtimeModule}
	for _, d := range t.Databases {
		names = append(names, d.Module)
	}
	sort.Strings(names)
	return names
}

// dbView is one database: <db>/mod.rs, which selects it, and its README.
type dbView struct {
	Header       string
	Name         string // the InfluxDB database and Arrow capture directory
	Module       string
	Lib          string
	RuntimeCrate string
	RuntimeGit   string
	RuntimeRef   string
	Schemas      []*moduleView
}

// Mods lists the schema modules as rustfmt orders them.
func (d *dbView) Mods() []string {
	var names []string
	for _, m := range d.Schemas {
		names = append(names, m.Module)
	}
	sort.Strings(names)
	return names
}

// Models lists every model with its schema module, in output order.
func (d *dbView) Models() []modelRef {
	var out []modelRef
	for _, m := range d.Schemas {
		for _, md := range m.Models {
			out = append(out, modelRef{Module: m.Module, Model: md})
		}
	}
	return out
}

type modelRef struct {
	Module string
	Model  *modelView
}

// ResourcesInline reports whether resources()'s vec! fits on one line, which
// rustfmt prefers below its 60-column array width.
func (d *dbView) ResourcesInline() bool {
	var items []string
	for _, r := range d.Models() {
		items = append(items, r.Module+"::"+r.Model.Name+"::resource()")
	}
	return len(strings.Join(items, ", ")) <= 60
}

// moduleView is one schema: <db>/<schema>/mod.rs, models.rs and its stores.
type moduleView struct {
	Header   string
	Database string
	Schema   string
	Module   string
	Source   string
	Models   []*modelView
	Enums    []*enumView
}

// Files lists the schema module's submodules as rustfmt orders them.
func (m *moduleView) Files() []string {
	names := []string{"models"}
	for _, md := range m.Models {
		names = append(names, md.StoreFile)
	}
	sort.Strings(names)
	return names
}

// treeOf builds the tree, or nil when no table declares a time series.
func treeOf(dbs []*schema.Database, fx facets.Set) (*treeView, error) {
	tv := &treeView{Crate: defaultCrate, RuntimeCrate: RuntimeCrate, RuntimeGit: RuntimeGit, RuntimeRef: RuntimeBranch}
	used := map[string]bool{runtimeModule: true}
	for _, db := range dbs {
		if c := db.Opt("rust_crate"); c != "" {
			tv.Crate = c
		}
		dv, err := dbOf(db, fx)
		if err != nil {
			return nil, err
		}
		if dv == nil {
			continue
		}
		dv.Module = naming.Unique(moduleName(db.Name), used)
		tv.Databases = append(tv.Databases, dv)
	}
	if len(tv.Databases) == 0 {
		return nil, nil
	}
	if !crateName.MatchString(tv.Crate) {
		return nil, fmt.Errorf("influxdb: crate %q is not a valid Cargo package name (a letter, then letters, digits, - and _)", tv.Crate)
	}
	tv.Lib = strings.ReplaceAll(tv.Crate, "-", "_")
	for _, d := range tv.Databases {
		d.Lib, d.RuntimeCrate, d.RuntimeGit, d.RuntimeRef = tv.Lib, tv.RuntimeCrate, tv.RuntimeGit, tv.RuntimeRef
	}
	return tv, nil
}

var crateName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// dbOf builds one database's modules, or nil when it declares no time series.
//
// InfluxDB has databases and tables but no schemas, so a schema only names a
// Rust module: every table of the database shares one namespace, and two
// schemas declaring the same table would write into one table. That is refused
// here rather than discovered as mixed points.
func dbOf(db *schema.Database, fx facets.Set) (*dbView, error) {
	dv := &dbView{Name: db.Name}
	tables := map[string]string{}
	used := map[string]bool{}
	for _, s := range db.Schemas {
		mv := &moduleView{Database: db.Name, Schema: s.Name}
		var sources []string
		var enums []protoreflect.EnumDescriptor
		for _, t := range s.Tables {
			ts := fx.Table(t).GetTimeSeries()
			if ts == nil {
				continue
			}
			m, err := modelOf(t, ts)
			if err != nil {
				return nil, err
			}
			if prev, ok := tables[m.Table]; ok {
				return nil, fmt.Errorf("influxdb: %s and %s both store table %q in database %q; "+
					"InfluxDB has no schemas, so rename one with (entity.v1.table)", prev, m.Proto, m.Table, db.Name)
			}
			tables[m.Table] = m.Proto
			mv.Models = append(mv.Models, m)
			for _, f := range m.Fields {
				if f.enum != nil && !slices.Contains(enums, f.enum) {
					enums = append(enums, f.enum)
				}
			}
			if t.SourceProto != "" && !slices.Contains(sources, t.SourceProto) {
				sources = append(sources, t.SourceProto)
			}
		}
		if len(mv.Models) == 0 {
			continue
		}
		sort.Strings(sources)
		mv.Source = strings.Join(sources, ", ")
		names := map[string]bool{}
		for _, m := range mv.Models {
			names[m.Name], names[m.Store], names[m.TagEnum], names[m.FieldEnum] = true, true, true, true
		}
		renames := map[protoreflect.FullName]string{}
		for _, ed := range enums {
			e := enumOf(ed, names)
			mv.Enums = append(mv.Enums, e)
			renames[ed.FullName()] = e.Name
		}
		for _, m := range mv.Models {
			for i, f := range m.Fields {
				if f.enum != nil {
					m.Fields[i].Comment = joinComment(f.Comment, fmt.Sprintf("A [`%s`] number.", renames[f.enum.FullName()]), "number")
				}
			}
		}
		mv.Module = naming.Unique(moduleName(s.Name), used)
		dv.Schemas = append(dv.Schemas, mv)
	}
	if len(dv.Schemas) == 0 {
		return nil, nil
	}
	return dv, nil
}
