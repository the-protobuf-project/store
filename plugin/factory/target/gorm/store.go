package gorm

// store.go prepares the per-resource store template views: one typed CRUD store
// per table, generated from the same Table IR that drives the structs in
// view.go. The generated surface mirrors a Hasura/PostGraphile-style auto-CRUD
// API (create, get-by-id, list with limit/offset/order/where, count, update,
// delete-by-id) plus typed finders (GetBy<Col> for unique columns, ListBy<FK>
// for foreign keys) — derived generically from each resource, no per-proto code.
//
// Stores live in the models package (referencing the model types directly) but
// are emitted one file per model to keep each small, and import the shared gormx
// runtime (ListOptions, the generic Store interface) by its <go_module>/gormx
// path — so the stores opt now also requires the go_module opt. Opt-in via the
// stores plugin opt: it introduces a gorm.io/gorm dependency.

import (
	"sort"
	"strings"

	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/target/types"
)

// gormxImportPath is the import path of the shared gormx runtime package, emitted
// at <go_module>/gormx. Stores import its ListOptions and Store interface.
func gormxImportPath(db *schema.Database) string {
	return dbGoModule(db) + "/gormx"
}

// finderView is one typed finder method: GetBy<Col> for a unique column or
// ListBy<Col> for a foreign-key column.
type finderView struct {
	Method  string // Go method name, e.g. "GetByName" or "ListByAuthorID"
	Column  string // db column name used in the WHERE clause
	ArgType string // Go type of the lookup argument (never a pointer)
	Op      string // op label on the instrumented metric, e.g. "get_by_name"
}

// gormxView assembles the data for the shared gormx runtime package, emitted
// once at <go_module>/gormx. db supplies the version banner; the type/interface
// bodies are static.
func gormxView(db *schema.Database) map[string]any {
	return map[string]any{
		"Header": header.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Database:      db.Name,
			SchemaLabel:   "package",
			Schema:        gormxPkg,
			Notes:         []string{"Shared GORM runtime: ListOptions, the generic Store interface, the GenericStore engine, and EnsureSchemas."},
		}),
		"Package": gormxPkg,
	}
}

// storeModelView assembles the data for one resource's <model>_store.go file.
func storeModelView(db *schema.Database, s *schema.Schema, pkg string, t *schema.Table, typeOf types.TypeOf) map[string]any {
	var unique, fks []finderView
	var pkArgType string
	hasPK := false
	uniqueCols := map[string]bool{} // columns already given a GetBy finder
	colByName := map[string]*schema.Column{}
	for _, col := range t.Columns {
		colByName[col.Name] = col
		if col.Name == t.PKColumn {
			hasPK = true
			pkArgType = baseGoType(col, typeOf)
		}
		if col.Unique && !col.PrimaryKey {
			unique = append(unique, finderView{
				Method:  "GetBy" + gormFieldName(col),
				Column:  col.Name,
				ArgType: baseGoType(col, typeOf),
				Op:      "get_by_" + col.Name,
			})
			uniqueCols[col.Name] = true
		}
		if col.FKModel != "" {
			fks = append(fks, finderView{
				Method:  "ListBy" + gormFieldName(col),
				Column:  col.Name,
				ArgType: baseGoType(col, typeOf),
				Op:      "list_by_" + col.Name,
			})
		}
	}
	// A column made unique via a single-column unique index gets a finder too —
	// table-level uniqueness is as good a lookup key as column.unique (e.g. a
	// `code` column declared in orm.v1.table.indexes). Deduped against the
	// column.unique finders above; the PK is covered by GetByID.
	for _, idx := range t.Indexes {
		if !idx.Unique || len(idx.Columns) != 1 {
			continue
		}
		name := idx.Columns[0]
		col := colByName[name]
		if col == nil || uniqueCols[name] || name == t.PKColumn {
			continue
		}
		unique = append(unique, finderView{
			Method:  "GetBy" + gormFieldName(col),
			Column:  col.Name,
			ArgType: baseGoType(col, typeOf),
			Op:      "get_by_" + col.Name,
		})
		uniqueCols[name] = true
	}

	// Stores import the shared gormx runtime (ListOptions, Store) alongside gorm.
	third := []string{gormxImportPath(db), "gorm.io/gorm"}
	sort.Strings(third)

	// Telemetry: when the tree opt is on and the table doesn't opt out, every
	// method body is wrapped in a span and (with metrics) records a DB-scoped op
	// metric (table/op/status only — no domain-value metrics), which needs
	// "time" for the duration.
	telEnabled, telMetrics, spanPrefix := tableTelemetry(db, s, t)
	std := []string{"context"}
	if telEnabled && telMetrics {
		std = append(std, "time")
	}

	return map[string]any{
		"Header":  storeHeader(db, s),
		"Package": pkg,
		"Imports": importBlock(std, third),
		"Comment": commentOr(t.Comment, t.LocalName+" model."),
		"Name":    t.LocalName,
		"Store":   t.LocalName + "Store",
		// The interface every decorator (cache, streams, a test double) is written
		// against, so none of them needs to edit generated code in this tree.
		"Iface":     t.LocalName + "StoreIface",
		"TableName": s.Name + "." + t.Name,
		"HasPK":     hasPK,
		"PKColumn":  t.PKColumn,
		"PKArgType": pkArgType,
		// The generic gormx.Store fixes id as a string, so only string-PK stores
		// (orm's ULID/UUID default) can assert they satisfy it.
		"AssertStore":   hasPK && pkArgType == "string",
		"UniqueFinders": unique,
		"FKFinders":     fks,
		"Telemetry":     telEnabled,
		"Metrics":       telEnabled && telMetrics,
		"SpanPrefix":    spanPrefix,
	}
}

// storeFileName is the output base name for a table's store file
// (Author → "author_store.go").
func storeFileName(t *schema.Table) string {
	return naming.SnakeCase(t.LocalName) + "_store.go"
}

// baseGoType is the column's Go type with any nullable pointer stripped — lookup
// arguments take the plain value (a *string FK column is still queried by string).
func baseGoType(col *schema.Column, typeOf types.TypeOf) string {
	return strings.TrimPrefix(goType(col, typeOf), "*")
}

// storeHeader renders the generated-file banner shared by every store file in a
// schema package, matching the models.go banner.
func storeHeader(db *schema.Database, s *schema.Schema) string {
	return header.Render("//", header.Info{
		PluginVersion: db.PluginVersion,
		ProtocVersion: db.ProtocVersion,
		Source:        strings.Join(s.SourceProtos(), ", "),
		Database:      db.Name,
		Schema:        s.Name,
	})
}
