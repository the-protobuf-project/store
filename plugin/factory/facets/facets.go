// Package facets is the one place that knows how this plugin's annotation
// vocabularies are stored on protokit's IR, and the only way its targets read
// them back.
//
// protokit v1.2.0 keeps generator-specific options out of the neutral IR: a
// plugin's FacetReader attaches values to a side-table keyed by NodeID (the
// fully-qualified proto name), and targets look them up with protokit.Facet.
// The readers that produce these values live in factory/source/proto/backend;
// this package owns the keys, the stored types, and the accessors, so the two
// sides cannot drift.
//
// Reading a facet rather than the column's Source descriptor is what makes the
// options survive the build: a synthesized column (a surrogate key, an embedded
// child's foreign key) carries no descriptor at all, and a renamed table would
// otherwise orphan whatever was hung off its name.
//
// # Two vocabularies
//
// store.v1 is current. orm.v1 is the deprecated predecessor, still read for one
// major so existing protos keep generating. Every accessor here resolves
// store.v1 first and falls back to the orm.v1 equivalent, mapped field for
// field — so a target never learns which vocabulary a schema was written in.
// The deprecation *warning* is not this package's job: protokit emits it from
// the compat reader's DeprecatedStructure marker, aggregated per option.
package facets

import (
	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/pb/ormpbv1"
	"github.com/the-protobuf-project/store/plugin/pb/storepbv1"
)

// Facet keys, one per vocabulary. They name the vocabulary, not the plugin —
// another generator reading our options names these strings.
const (
	// KeyStore is the current vocabulary.
	KeyStore = "store.v1"

	// KeyORM is the deprecated predecessor, read by the compat reader.
	// Removed one major after store.v1 ships.
	KeyORM = "orm.v1"
)

// Field is the facet attached to a field descriptor. A field may carry both the
// physical column options and the query-surface options, and protokit stores one
// value per node, so the two travel together. Either half may be nil.
type Field struct {
	Column *storepbv1.ColumnOptions
	Query  *storepbv1.QueryOptions
}

// ORMField is the deprecated orm.v1 equivalent of [Field].
type ORMField struct {
	Column *ormpbv1.ColumnOptions
	Query  *ormpbv1.QueryOptions
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
//
// store.v1 wins outright when present: a field that has been migrated is not
// also consulted for its legacy annotation, so a half-migrated field behaves
// like a migrated one rather than merging two sources of truth.
func (s Set) Column(col *schema.Column) *storepbv1.ColumnOptions {
	if col == nil {
		return &storepbv1.ColumnOptions{}
	}
	if f, ok := protokit.Facet[*Field](s.ir, KeyStore, col.Node); ok && f.Column != nil {
		return f.Column
	}
	if f, ok := protokit.Facet[*ORMField](s.ir, KeyORM, col.Node); ok && f.Column != nil {
		return columnFromORM(f.Column)
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
	if f, ok := protokit.Facet[*ORMField](s.ir, KeyORM, col.Node); ok && f.Query != nil {
		return queryFromORM(f.Query)
	}
	return &storepbv1.QueryOptions{}
}

// Table returns a table's storage options, never nil.
//
// There is no orm.v1 fallback: every field of the old orm.v1.table was neutral
// structure and moved to protokit.v1, so the only thing store.v1.table carries
// (outbox) has no legacy spelling to be compatible with.
func (s Set) Table(t *schema.Table) *storepbv1.TableOptions {
	if t == nil {
		return &storepbv1.TableOptions{}
	}
	if o, ok := protokit.Facet[*storepbv1.TableOptions](s.ir, KeyStore, t.Node); ok && o != nil {
		return o
	}
	return &storepbv1.TableOptions{}
}

// columnFromORM maps the deprecated orm.v1 physical column options onto their
// store.v1 equivalents. The two sets are identical in meaning — only the
// vocabulary and the field numbers changed — so this is a straight projection.
// orm.v1's column/skip are deliberately absent: those are neutral structure and
// went to protokit.v1, which the compat reader supplies as a StructureReader.
func columnFromORM(o *ormpbv1.ColumnOptions) *storepbv1.ColumnOptions {
	return &storepbv1.ColumnOptions{
		Type:         o.GetType(),
		DefaultValue: o.GetDefaultValue(),
		Unique:       o.GetUnique(),
		Index:        o.GetIndex(),
		MaxLength:    o.GetMaxLength(),
		Precision:    o.GetPrecision(),
		Scale:        o.GetScale(),
		OnDelete:     refActionFromORM(o.GetOnDelete()),
		OnUpdate:     refActionFromORM(o.GetOnUpdate()),
	}
}

// refActionFromORM maps a deprecated orm.v1 referential action onto store.v1's.
//
// Written out rather than cast through the underlying int: the two enums happen
// to number identically today, but they live in separate files that may drift,
// and a silent renumbering would turn ON DELETE CASCADE into SET NULL — a
// data-loss bug no test of the generated SQL's *shape* would catch.
func refActionFromORM(a ormpbv1.ReferentialAction) storepbv1.ReferentialAction {
	switch a {
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_CASCADE:
		return storepbv1.ReferentialAction_REFERENTIAL_ACTION_CASCADE
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_RESTRICT:
		return storepbv1.ReferentialAction_REFERENTIAL_ACTION_RESTRICT
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_SET_NULL:
		return storepbv1.ReferentialAction_REFERENTIAL_ACTION_SET_NULL
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_SET_DEFAULT:
		return storepbv1.ReferentialAction_REFERENTIAL_ACTION_SET_DEFAULT
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_NO_ACTION:
		return storepbv1.ReferentialAction_REFERENTIAL_ACTION_NO_ACTION
	default:
		return storepbv1.ReferentialAction_REFERENTIAL_ACTION_UNSPECIFIED
	}
}

// queryFromORM maps the deprecated orm.v1 query options onto store.v1's.
// filterable/sortable keep their explicit presence — an unset override means
// "use the type-derived default", which is not the same as false.
func queryFromORM(o *ormpbv1.QueryOptions) *storepbv1.QueryOptions {
	return &storepbv1.QueryOptions{
		Filterable: o.Filterable,
		Sortable:   o.Sortable,
		Search:     o.GetSearch(),
	}
}
