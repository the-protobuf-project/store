// Package facets is the one place that knows how this plugin's annotation
// vocabularies are stored on protokit's IR, and the only way its targets read
// them back.
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
	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/pb/storepbv1"
)

// KeyStore namespaces this plugin's facet side-table. It names the vocabulary,
// not the plugin — another generator reading our options names this string.
const KeyStore = "store.v1"

// Field is the facet attached to a field descriptor. A field may carry both the
// physical column options and the query-surface options, and protokit stores one
// value per node, so the two travel together. Either half may be nil.
type Field struct {
	Column *storepbv1.ColumnOptions
	Query  *storepbv1.QueryOptions
}

// Set is the per-run facet lookup a target resolves from the IR once and threads
// into its view builders.
//
// The zero Set is valid and reports every node as unannotated, which is what any
// caller that builds views from a bare []*schema.Database needs.
type Set struct{ ir *protokit.IR }

// New returns the Set reading ir's facets.
func New(ir *protokit.IR) Set { return Set{ir: ir} }

// Column returns a column's physical storage options, never nil.
func (s Set) Column(col *schema.Column) *storepbv1.ColumnOptions {
	if col == nil {
		return &storepbv1.ColumnOptions{}
	}
	if f, ok := protokit.Facet[*Field](s.ir, KeyStore, col.Node); ok && f.Column != nil {
		return f.Column
	}
	return &storepbv1.ColumnOptions{}
}

// Query returns a column's list-query surface options, never nil.
func (s Set) Query(col *schema.Column) *storepbv1.QueryOptions {
	if col == nil {
		return &storepbv1.QueryOptions{}
	}
	if f, ok := protokit.Facet[*Field](s.ir, KeyStore, col.Node); ok && f.Query != nil {
		return f.Query
	}
	return &storepbv1.QueryOptions{}
}

// Table returns a table's storage options, never nil.
func (s Set) Table(t *schema.Table) *storepbv1.TableOptions {
	if t == nil {
		return &storepbv1.TableOptions{}
	}
	if o, ok := protokit.Facet[*storepbv1.TableOptions](s.ir, KeyStore, t.Node); ok && o != nil {
		return o
	}
	return &storepbv1.TableOptions{}
}
