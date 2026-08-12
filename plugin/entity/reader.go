package entity

// reader.go is the schema.StructureReader over entity.v1 — the neutral half of
// the build, and the only source protokit has for it.
//
// The option accessors and the two enum/index conversions below were protokit's:
// they lived in its structure.go as pkDatasourceOpts, pkTableOpts, pkColumnOpts,
// pkIDStrategy, and pkIndexes, reading protokit.v1 directly during the build. They
// were moved here unchanged when the vocabulary moved, deliberately as a move
// rather than a rewrite. Every neutral name any generator has ever produced came
// out of this code; reimplementing it would have put "does the new reader agree
// with the old engine?" on the critical path of a change that was supposed to be
// about ownership.
//
// What changed is *where* it runs, not what it does. protokit used to read these
// options first and consult a StructureReader only for what they left unset; now
// this is a StructureReader like any other, resolved first because "entity.v1"
// sorts before the generator vocabularies that follow it.

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/entity/pb/entitypbv1"
)

// Key is the facet key entity.v1 is registered under. It names the vocabulary,
// not any plugin — a target reading these values names this string.
//
// It sorts before a generator's own vocabulary ("store.v1", "web3.v1") and before
// [CompatKey], which is what makes an explicit entity.v1 annotation win over both.
const Key = "entity.v1"

// Reader returns the reader for entity.v1: a protokit.FacetReader that is also a
// schema.StructureReader, supplying the neutral structure of every file, message,
// and field.
//
// Every plugin that wants the neutral names registers this exact value. See the
// package doc for why writing your own is a mistake rather than a preference.
func Reader() protokit.FacetReader { return reader{} }

// reader is stateless: entity.v1 is read straight off the descriptors, with no
// config and no per-run state. The zero value is the whole thing.
type reader struct{}

// Compile-time proof that reader satisfies both halves of the seam.
var (
	_ schema.FacetReader     = reader{}
	_ schema.StructureReader = reader{}
)

// Key namespaces this reader's facets.
func (reader) Key() string { return Key }

// --- schema.FacetReader ---
//
// entity.v1 contributes no facets, and that is not an oversight. A facet is a
// value protokit stores without interpreting, for a target to read back at render
// time. Everything entity.v1 expresses is structure protokit acts on *during* the
// build — it lands in the IR as a table name, a column name, a synthesized key —
// so by the time a target runs there is nothing left to look up. Attaching the raw
// options as well would give a target two sources for one answer, and the wrong
// one would be authoritative-looking.

func (reader) ReadFile(protoreflect.FileDescriptor) (any, error)       { return nil, nil }
func (reader) ReadMessage(protoreflect.MessageDescriptor) (any, error) { return nil, nil }
func (reader) ReadField(protoreflect.FieldDescriptor) (any, error)     { return nil, nil }

// --- schema.StructureReader ---

// ReadDatasource maps (entity.v1.datasource) onto the neutral grouping.
//
// SchemaStrip stays false: a schema named outright in an annotation is
// authoritative and is never version-stripped. Stripping is the layout config's
// decision, and it applies only to names protokit derived (see [Layout]).
func (reader) ReadDatasource(d protoreflect.FileDescriptor) schema.Datasource {
	o := datasourceOpts(d)
	return schema.Datasource{
		Database: o.GetDatabase(),
		Schema:   o.GetSchema(),
		URL:      o.GetUrl(),
		Provider: o.GetProvider(),
	}
}

// ReadTable maps (entity.v1.table) onto the neutral table structure.
func (reader) ReadTable(d protoreflect.MessageDescriptor) schema.TableStructure {
	o := tableOpts(d)
	return schema.TableStructure{
		Table:      o.GetTable(),
		Skip:       o.GetSkip(),
		ID:         idStrategy(o.GetId()),
		Timestamps: o.GetTimestamps(),
		Indexes:    indexes(o.GetIndexes()),
	}
}

// ReadColumn maps (entity.v1.column) onto the neutral column structure.
//
// OnDelete and OnUpdate stay empty: referential actions are a storage guarantee,
// not a name, and they come from the generator's own vocabulary (store.v1 supplies
// them). entity.v1 deliberately does not express them.
func (reader) ReadColumn(d protoreflect.FieldDescriptor) schema.ColumnStructure {
	o := columnOpts(d)
	return schema.ColumnStructure{
		Column: o.GetColumn(),
		Skip:   o.GetSkip(),
	}
}

// --- option accessors ---
//
// Each returns an empty message when the descriptor is nil or unannotated, so
// every caller can read through the getters without a presence check.

func datasourceOpts(d protoreflect.FileDescriptor) *entitypbv1.DatasourceOptions {
	if d == nil || !proto.HasExtension(d.Options(), entitypbv1.E_Datasource) {
		return &entitypbv1.DatasourceOptions{}
	}
	return proto.GetExtension(d.Options(), entitypbv1.E_Datasource).(*entitypbv1.DatasourceOptions)
}

func tableOpts(d protoreflect.MessageDescriptor) *entitypbv1.TableOptions {
	if d == nil || !proto.HasExtension(d.Options(), entitypbv1.E_Table) {
		return &entitypbv1.TableOptions{}
	}
	return proto.GetExtension(d.Options(), entitypbv1.E_Table).(*entitypbv1.TableOptions)
}

func columnOpts(d protoreflect.FieldDescriptor) *entitypbv1.ColumnOptions {
	if d == nil || !proto.HasExtension(d.Options(), entitypbv1.E_Column) {
		return &entitypbv1.ColumnOptions{}
	}
	return proto.GetExtension(d.Options(), entitypbv1.E_Column).(*entitypbv1.ColumnOptions)
}

// idStrategy maps entity.v1's IdStrategy onto the neutral schema.IDStrategy.
func idStrategy(s entitypbv1.IdStrategy) schema.IDStrategy {
	switch s {
	case entitypbv1.IdStrategy_ID_STRATEGY_ULID:
		return schema.IDULID
	case entitypbv1.IdStrategy_ID_STRATEGY_UUID:
		return schema.IDUUID
	default:
		return schema.IDUnspecified
	}
}

// indexes converts entity.v1 index declarations to the neutral IR form.
func indexes(defs []*entitypbv1.IndexDef) []*schema.Index {
	if len(defs) == 0 {
		return nil
	}
	out := make([]*schema.Index, 0, len(defs))
	for _, d := range defs {
		out = append(out, &schema.Index{
			Name:    d.GetIndex(),
			Columns: d.GetColumns(),
			Unique:  d.GetUnique(),
		})
	}
	return out
}
