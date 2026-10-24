// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

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
	"strconv"
	"strings"

	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
	"github.com/the-protobuf-project/store/plugin/pb/storepbv1"
)

// TrigramExtension is the PostgreSQL extension supplying gin_trgm_ops. A
// database gets it via CREATE EXTENSION before any trigram index is created.
const TrigramExtension = "pg_trgm"

// TrigramOps is the operator class making a GIN index answer ILIKE.
const TrigramOps = "gin_trgm_ops"

// Plan is one GIN (or GiST) index derived from a table's query surface.
type Plan struct {
	Name   string // idx_<table>_<column>_trgm / _gin / _gist
	Column string // the single column indexed; for an expression index, the column its tag rides on
	// Method is the access method: "gin" (the default when empty) or "gist".
	Method string
	// Columns, for a multi-column index, are every indexed column in order
	// (Column is the first).
	Columns []string
	// Expression, when set, is indexed verbatim instead of Column.
	Expression string
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
	proj := fx.Projection(t)
	for _, c := range t.Columns {
		if proj != nil && c.Source != nil && string(c.Source.Name()) == proj.Options.GetRangeField() {
			out = append(out, Plan{
				Name:   indexName(t.Name, c.Name, "gist"),
				Column: c.Name,
				Method: MethodGiST,
				Why:    "range containment (@>) cannot use a B-tree",
			})
			continue
		}
		if c.Source != nil && c.Type == schema.TypeJSON {
			q := fx.Query(c)
			if proj != nil || len(q.GetJsonKeys()) > 0 || (q.Filterable != nil && *q.Filterable) {
				out = append(out, Plan{
					Name:   indexName(t.Name, c.Name, "gin"),
					Column: c.Name,
					Why:    "jsonb containment (@>) on attribute paths cannot use a B-tree",
				})
				continue
			}
		}
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
		case c.List && (c.Type == schema.TypeString || c.Type == schema.TypeText) && c.FKModel == "":
			out = append(out, Plan{
				Name:   indexName(t.Name, c.Name, "gin"),
				Column: c.Name,
				Why:    "tag containment (@>) cannot use a B-tree",
			})

		// A text column exposed to free-text search, which compiles to a
		// leading-wildcard ILIKE.
		// A projection's text columns are TEXT rather than VARCHAR.
		case q.GetSearch() && (c.Type == schema.TypeString || proj != nil && c.Type == schema.TypeText) && !c.List && c.FKModel == "":
			out = append(out, Plan{
				Name:      indexName(t.Name, c.Name, "trgm"),
				Column:    c.Name,
				Ops:       TrigramOps,
				Extension: TrigramExtension,
				Why:       "case-insensitive contains (ILIKE '%…%') cannot use a B-tree",
			})
		}
	}
	if proj != nil {
		out = append(out, declared(t, proj)...)
	}
	return out
}

// Access methods a plan may name.
const (
	MethodGIN  = "gin"
	MethodGiST = "gist"
)

// declared returns the GIN/GiST and expression indexes a projection declares
// (store.v1.projection indexes); its B-tree column indexes are ordinary
// schema.Index entries.
func declared(t *schema.Table, p *facets.Projection) []Plan {
	var out []Plan
	for i, idx := range p.Options.GetIndexes() {
		method := ""
		switch idx.GetMethod() {
		case storepbv1.IndexMethod_INDEX_METHOD_GIN:
			method = MethodGIN
		case storepbv1.IndexMethod_INDEX_METHOD_GIST:
			method = MethodGiST
		default:
			if idx.GetExpression() == "" {
				continue
			}
			method = "btree"
		}
		var cols []string
		for _, f := range idx.GetFields() {
			for _, c := range t.Columns {
				if c.Source != nil && string(c.Source.Name()) == f {
					cols = append(cols, c.Name)
				}
			}
		}
		pl := Plan{Name: idx.GetIndex(), Method: method, Expression: idx.GetExpression(), Why: "declared by store.v1.projection"}
		switch {
		case pl.Expression != "":
			pl.Column = t.PKColumn
		case len(cols) == 1:
			pl.Column = cols[0]
		case len(cols) > 1:
			pl.Column, pl.Columns = cols[0], cols
		default:
			continue
		}
		if pl.Name == "" {
			pl.Name = "idx_" + t.Name + "_" + strings.Join(cols, "_") + "_" + method
			if len(cols) == 0 {
				pl.Name = "idx_" + t.Name + "_expr" + strconv.Itoa(i+1) + "_" + method
			}
		}
		out = append(out, pl)
	}
	return out
}

// MethodOf is p's access method, "gin" when unset.
func (p Plan) MethodOf() string {
	if p.Method == "" {
		return MethodGIN
	}
	return p.Method
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
