// Package facets is the one place that knows how this plugin's own annotation
// vocabulary is stored on protokit's IR, and the only way its targets read it
// back.
//
// protokit v1.2.0 keeps generator-specific options out of the neutral IR: a
// plugin's FacetReader attaches values to a side-table keyed by NodeID (the
// fully-qualified proto name), and targets look them up with protokit.Facet.
// The reader that produces these values lives in factory/source/proto/backend;
// this package owns the key, the stored types, and the accessors, so the two
// sides cannot drift.
//
// Reading a facet rather than the column's Source descriptor is what makes the
// options survive the build: a synthesized column (a surrogate key, an embedded
// child's foreign key) carries no descriptor at all, and a renamed table would
// otherwise orphan whatever was hung off its name.
package facets

import (
	"github.com/the-protobuf-project/orm/plugin/pb/ormpbv1"
	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/schema"
)

// Key namespaces this plugin's facet side-table. It is the vocabulary name, not
// the plugin name — another plugin reading our options names this string.
const Key = "orm.v1"

// Field is the facet attached to a field descriptor. A field may carry both the
// physical column options and the query-surface options, and protokit stores one
// value per node, so the two travel together. Either half may be nil.
type Field struct {
	Column *ormpbv1.ColumnOptions
	Query  *ormpbv1.QueryOptions
}

// Set is the per-run facet lookup a target resolves from the IR once and threads
// into its view builders.
//
// The zero Set is valid and reports every node as unannotated, which is what the
// deprecated schema.Backend path (and any test that builds views from a bare
// []*schema.Database) needs.
type Set struct{ ir *protokit.IR }

// New returns the Set reading ir's facets under [Key].
func New(ir *protokit.IR) Set { return Set{ir: ir} }

// Column returns a column's orm.v1 column options, never nil. A synthesized
// column, or one whose field carried no annotation, yields the empty value.
func (s Set) Column(col *schema.Column) *ormpbv1.ColumnOptions {
	if col == nil {
		return &ormpbv1.ColumnOptions{}
	}
	if f, ok := protokit.Facet[*Field](s.ir, Key, col.Node); ok && f.Column != nil {
		return f.Column
	}
	return &ormpbv1.ColumnOptions{}
}

// Query returns a column's orm.v1 query options, never nil.
func (s Set) Query(col *schema.Column) *ormpbv1.QueryOptions {
	if col == nil {
		return &ormpbv1.QueryOptions{}
	}
	if f, ok := protokit.Facet[*Field](s.ir, Key, col.Node); ok && f.Query != nil {
		return f.Query
	}
	return &ormpbv1.QueryOptions{}
}

// Table returns a table's orm.v1 table options, never nil.
func (s Set) Table(t *schema.Table) *ormpbv1.TableOptions {
	if t == nil {
		return &ormpbv1.TableOptions{}
	}
	if o, ok := protokit.Facet[*ormpbv1.TableOptions](s.ir, Key, t.Node); ok && o != nil {
		return o
	}
	return &ormpbv1.TableOptions{}
}
