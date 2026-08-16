// Package searchindex plans the GIN indexes a table's query surface needs.
//
// The filter engine's containment operators cannot use a B-tree: a leading
// wildcard (`col ILIKE '%term%'`, what `:` and free-text search compile to) has
// no prefix for the tree to descend, and array containment (`col @> ARRAY[?]`)
// is not a B-tree operator at all. Both degrade to a sequential scan on every
// query, which is precisely the surface a list endpoint exercises most.
//
// These indexes cannot live in schema.Index — it carries only a name, columns
// and uniqueness, with no access method or operator class, and it belongs to
// protokit. So the plan is derived here from the same store.v1.query facets that
// drive the filter specs, and each target renders it in its own syntax. One
// derivation and three renderers keeps the backends in agreement by
// construction, rather than by three targets independently guessing alike.
package searchindex

import (
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
)

// TrigramExtension is the PostgreSQL extension supplying gin_trgm_ops. A
// database gets it via CREATE EXTENSION before any trigram index is created.
const TrigramExtension = "pg_trgm"

// TrigramOps is the operator class making a GIN index answer ILIKE.
const TrigramOps = "gin_trgm_ops"

// Plan is one GIN index derived from a table's query surface.
type Plan struct {
	Name   string // idx_<table>_<column>_trgm / _gin
	Column string // the single column indexed
	// Ops is the operator class the column needs, or "" to take the type's
	// default. Text has no default GIN operator class, so a trigram index must
	// name one; array types do have one.
	Ops string
	// Extension is the PostgreSQL extension Ops comes from, or "" when none is
	// needed.
	Extension string
	// Why explains the query shape this index serves, rendered as a comment so
	// the generated DDL says what it is for.
	Why string
}

// For returns the plans for one table, in column order so every target emits
// them identically.
//
// Both rules follow the field's own store.v1.query settings, so an index is only
// planned for a surface the schema actually opted into: a trigram index needs
// `search: true`, and a tag index needs the column left filterable.
func For(t *schema.Table, fx facets.Set) []Plan {
	var out []Plan
	for _, c := range t.Columns {
		if c.Source == nil {
			continue // synthesized columns carry no query surface
		}
		q := fx.Query(c)

		filterable := true
		if q.Filterable != nil {
			filterable = *q.Filterable
		}
		if !filterable {
			continue
		}

		switch {
		// A text array filtered by containment. Array types have a default GIN
		// operator class, so no extension is involved.
		case c.List && c.Type == schema.TypeString && c.FKModel == "":
			out = append(out, Plan{
				Name:   indexName(t.Name, c.Name, "gin"),
				Column: c.Name,
				Why:    "tag containment (@>) cannot use a B-tree",
			})

		// A text column exposed to free-text search, which compiles to a
		// leading-wildcard ILIKE.
		case q.GetSearch() && c.Type == schema.TypeString && !c.List && c.FKModel == "":
			out = append(out, Plan{
				Name:      indexName(t.Name, c.Name, "trgm"),
				Column:    c.Name,
				Ops:       TrigramOps,
				Extension: TrigramExtension,
				Why:       "case-insensitive contains (ILIKE '%…%') cannot use a B-tree",
			})
		}
	}
	return out
}

// Extensions returns the distinct extensions every plan across dbs needs, so a
// target can create them before the indexes that use them.
func Extensions(plans []Plan) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range plans {
		if p.Extension == "" || seen[p.Extension] {
			continue
		}
		seen[p.Extension] = true
		out = append(out, p.Extension)
	}
	return out
}

// ForDatabase returns every table's plans across a database, paired with the
// schema and table they belong to.
func ForDatabase(db *schema.Database, fx facets.Set) []TablePlans {
	var out []TablePlans
	for _, s := range db.Schemas {
		for _, t := range s.Tables {
			if plans := For(t, fx); len(plans) > 0 {
				out = append(out, TablePlans{Schema: s, Table: t, Plans: plans})
			}
		}
	}
	return out
}

// TablePlans groups one table's plans with the schema that holds it.
type TablePlans struct {
	Schema *schema.Schema
	Table  *schema.Table
	Plans  []Plan
}

// indexName mirrors protokit's idx_<table>_<cols> scheme, suffixed with the
// access method so a GIN index never collides with the B-tree one a declared
// index or foreign key may already put on the same column.
func indexName(table, column, suffix string) string {
	return "idx_" + table + "_" + column + "_" + suffix
}
