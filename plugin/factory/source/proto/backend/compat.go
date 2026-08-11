package backend

// compat.go reads the deprecated orm.v1 vocabulary so schemas written before the
// protokit.v1 / store.v1 split keep generating, unchanged, for one major.
//
// orm.v1 said everything at once: what a table was called *and* how its columns
// were stored. The split moved the naming half into protokit.v1 (which protokit
// reads directly, so every plugin agrees on it) and the storage half into
// store.v1. This reader maps an orm.v1 annotation onto whichever side now owns
// it:
//
//	orm.v1.datasource{database,schema,url,provider}  → protokit.v1.datasource
//	orm.v1.table{table,skip,id,timestamps,indexes}   → protokit.v1.table
//	orm.v1.column{column,skip}                       → protokit.v1.column
//	orm.v1.column{type,max_length,…,on_delete,…}     → store.v1.column
//	orm.v1.query{filterable,sortable,search}         → store.v1.query
//
// The first three are *structure* — protokit acts on them while building — so
// they arrive through StructureReader. The last two are facets, read back by the
// targets through facets.Set, which falls back to this reader's key.
//
// [Compat] is marked schema.DeprecatedStructure, so protokit emits one warning
// per (vocabulary, option) pair per build naming the protokit.v1 option that
// replaces it — aggregated rather than per node, because a deprecation that
// prints a line per field is one people silence instead of act on.

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
	"github.com/the-protobuf-project/store/plugin/pb/ormpbv1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Compat reads the deprecated orm.v1 vocabulary. It is a FacetReader (for the
// physical options the targets read back) and a DeprecatedStructure (for the
// naming options protokit now owns).
//
// It is deliberately *not* an Enricher: [Reader] is the run's only one, so the
// per-database knobs are stamped once no matter which vocabularies a schema uses.
type Compat struct{}

var (
	_ schema.FacetReader         = Compat{}
	_ schema.DeprecatedStructure = Compat{}
)

// NewCompat returns the orm.v1 compatibility reader.
func NewCompat() Compat { return Compat{} }

// Key namespaces this reader's facets under the vocabulary it reads.
func (Compat) Key() string { return facets.KeyORM }

// StructureDeprecation is the clause protokit appends to each deprecation
// diagnostic, after an em dash.
func (Compat) StructureDeprecation() string {
	return "orm.v1 is superseded by protokit.v1 (structure) and store.v1 (storage), and is removed in v2"
}

// --- schema.FacetReader ---

// ReadFile attaches the file's orm.v1.datasource options. They are structure,
// not storage, so nothing reads this facet back — but contributing it keeps the
// vocabulary's presence visible to anything walking the IR's facet tables.
func (Compat) ReadFile(d protoreflect.FileDescriptor) (any, error) {
	if d == nil || !proto.HasExtension(d.Options(), ormpbv1.E_Datasource) {
		return nil, nil
	}
	return ormDatasourceOpts(d), nil
}

// ReadMessage attaches the message's orm.v1.table options, or nothing.
func (Compat) ReadMessage(d protoreflect.MessageDescriptor) (any, error) {
	if d == nil || !proto.HasExtension(d.Options(), ormpbv1.E_Table) {
		return nil, nil
	}
	return ormTableOpts(d), nil
}

// ReadField attaches the field's orm.v1.column and orm.v1.query options as one
// [facets.ORMField], which facets.Set maps onto their store.v1 equivalents when
// the field carries no store.v1 annotation of its own.
func (Compat) ReadField(d protoreflect.FieldDescriptor) (any, error) {
	if d == nil {
		return nil, nil
	}
	hasCol := proto.HasExtension(d.Options(), ormpbv1.E_Column)
	hasQuery := proto.HasExtension(d.Options(), ormpbv1.E_Query)
	if !hasCol && !hasQuery {
		return nil, nil
	}
	f := &facets.ORMField{}
	if hasCol {
		f.Column = ormColumnOpts(d)
	}
	if hasQuery {
		f.Query = ormQueryOpts(d)
	}
	return f, nil
}

// --- schema.DeprecatedStructure ---

// ReadDatasource maps orm.v1.datasource onto protokit's neutral Datasource.
//
// It returns the annotation only. Merging store.yaml in here is what the old
// Backend did, and it is why two plugins over one proto could disagree about a
// schema name; the config now arrives separately through [Layout]. SchemaStrip
// stays false because an explicitly annotated schema is authoritative.
func (Compat) ReadDatasource(d protoreflect.FileDescriptor) schema.Datasource {
	o := ormDatasourceOpts(d)
	return schema.Datasource{
		Database: o.GetDatabase(),
		Schema:   o.GetSchema(),
		Provider: o.GetProvider(),
		URL:      o.GetUrl(),
	}
}

// ReadTable maps orm.v1.table onto protokit's TableStructure, composite indexes
// included: protokit appends declared indexes before it synthesizes foreign-key
// indexes, so an index that already covers an FK column suppresses the redundant
// single-column one.
func (Compat) ReadTable(d protoreflect.MessageDescriptor) schema.TableStructure {
	o := ormTableOpts(d)
	return schema.TableStructure{
		Table:      o.GetTable(),
		Skip:       o.GetSkip(),
		ID:         ormIDStrategy(o.GetId()),
		Timestamps: o.GetTimestamps(),
		Indexes:    ormIndexes(o.GetIndexes()),
	}
}

// ReadColumn maps orm.v1.column's structural fields onto protokit's
// ColumnStructure.
//
// A field that already carries a store.v1.column annotation gets nothing from
// here. protokit takes the first non-empty value across structure readers in
// sorted key order, and "orm.v1" sorts before "store.v1" — so without this
// check a half-migrated field would silently keep using its legacy referential
// actions while appearing to have been migrated. Standing down is also what
// facets.Set does for the physical options, so both halves agree on the rule:
// once a field speaks store.v1, its orm.v1 annotation is inert.
func (Compat) ReadColumn(d protoreflect.FieldDescriptor) schema.ColumnStructure {
	if hasStoreColumn(d) {
		return schema.ColumnStructure{}
	}
	o := ormColumnOpts(d)
	return schema.ColumnStructure{
		Column:   o.GetColumn(),
		Skip:     o.GetSkip(),
		OnDelete: ormRefAction(o.GetOnDelete()),
		OnUpdate: ormRefAction(o.GetOnUpdate()),
	}
}

// WarnDeprecatedStorage reports the deprecated orm.v1 *storage* options this
// build actually used, one line per option, to w.
//
// protokit raises the structural deprecations itself, from the
// DeprecatedStructure marker above — but it can only warn about what it knows,
// and it knows nothing of a facet's contents. The storage half (type, sizing,
// constraints, the query surface) would otherwise migrate in silence, leaving an
// author who fixed every warning still on the old vocabulary.
//
// Aggregated per option with a count and an example node, matching protokit's
// own format, so the two halves read as one migration prompt rather than two
// unrelated ones. A column that already carries a store.v1 annotation is skipped:
// its orm.v1 fields are inert, which is what facets.Set and ReadColumn also do.
func WarnDeprecatedStorage(ir *protokit.IR, w io.Writer) {
	type note struct {
		count int
		first schema.NodeID
	}
	seen := map[string]*note{}
	record := func(option string, id schema.NodeID) {
		n, ok := seen[option]
		if !ok {
			n = &note{first: id}
			seen[option] = n
		}
		n.count++
	}

	for _, db := range ir.Databases {
		for _, s := range db.Schemas {
			for _, t := range s.Tables {
				for _, c := range t.Columns {
					// A migrated column says nothing about the old vocabulary.
					if f, ok := protokit.Facet[*facets.Field](ir, facets.KeyStore, c.Node); ok && f != nil {
						continue
					}
					f, ok := protokit.Facet[*facets.ORMField](ir, facets.KeyORM, c.Node)
					if !ok || f == nil {
						continue
					}
					for _, o := range setColumnOptions(f.Column) {
						record("column."+o, c.Node)
					}
					for _, o := range setQueryOptions(f.Query) {
						record("query."+o, c.Node)
					}
				}
			}
		}
	}

	for _, option := range sortedKeys(seen) {
		n := seen[option]
		where := string(n.first)
		if n.count > 1 {
			where = fmt.Sprintf("%s and %d other node(s)", n.first, n.count-1)
		}
		// Best effort: a warning that cannot be written is not worth failing a
		// build over, and w is stderr in every real caller.
		_, _ = fmt.Fprintf(w, "protoc-gen-store: warning: [lint] orm.v1 sets %s on %s; use store.v1.%s instead — %s\n",
			shortOption(option), where, option, Compat{}.StructureDeprecation())
	}
}

// setColumnOptions names the deprecated physical options actually set on o.
func setColumnOptions(o *ormpbv1.ColumnOptions) []string {
	if o == nil {
		return nil
	}
	var out []string
	for _, c := range []struct {
		name string
		set  bool
	}{
		{"type", o.GetType() != ""},
		{"default_value", o.GetDefaultValue() != ""},
		{"unique", o.GetUnique()},
		{"index", o.GetIndex()},
		{"max_length", o.GetMaxLength() > 0},
		{"precision", o.GetPrecision() > 0},
		{"scale", o.GetScale() > 0},
		{"on_delete", o.GetOnDelete() != ormpbv1.ReferentialAction_REFERENTIAL_ACTION_UNSPECIFIED},
		{"on_update", o.GetOnUpdate() != ormpbv1.ReferentialAction_REFERENTIAL_ACTION_UNSPECIFIED},
	} {
		if c.set {
			out = append(out, c.name)
		}
	}
	return out
}

// setQueryOptions names the deprecated query options actually set on o.
// filterable/sortable are checked for presence, not truth: an explicit `false`
// is a real override and must be migrated too.
func setQueryOptions(o *ormpbv1.QueryOptions) []string {
	if o == nil {
		return nil
	}
	var out []string
	if o.Filterable != nil {
		out = append(out, "filterable")
	}
	if o.Sortable != nil {
		out = append(out, "sortable")
	}
	if o.GetSearch() {
		out = append(out, "search")
	}
	return out
}

// sortedKeys returns m's keys in sorted order, so the warnings are stable across
// runs (the determinism harness compares two builds byte for byte).
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// shortOption is the bare option name for a "column.max_length"-style path.
func shortOption(option string) string {
	if i := strings.LastIndexByte(option, '.'); i >= 0 {
		return option[i+1:]
	}
	return option
}

// --- mapping helpers ---

// ormIDStrategy maps orm.v1.IdStrategy onto protokit's neutral schema.IDStrategy.
func ormIDStrategy(s ormpbv1.IdStrategy) schema.IDStrategy {
	switch s {
	case ormpbv1.IdStrategy_ID_STRATEGY_ULID:
		return schema.IDULID
	case ormpbv1.IdStrategy_ID_STRATEGY_UUID:
		return schema.IDUUID
	default:
		return schema.IDUnspecified
	}
}

// ormIndexes converts orm.v1's composite index declarations to protokit's
// neutral form.
func ormIndexes(defs []*ormpbv1.IndexDef) []*schema.Index {
	if len(defs) == 0 {
		return nil
	}
	out := make([]*schema.Index, 0, len(defs))
	for _, d := range defs {
		out = append(out, &schema.Index{
			Name: d.GetIndex(), Columns: d.GetColumns(), Unique: d.GetUnique(),
		})
	}
	return out
}

// ormRefAction converts an orm.v1.ReferentialAction to its SQL clause form.
func ormRefAction(a ormpbv1.ReferentialAction) string {
	switch a {
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_CASCADE:
		return "CASCADE"
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_RESTRICT:
		return "RESTRICT"
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_SET_NULL:
		return "SET NULL"
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_SET_DEFAULT:
		return "SET DEFAULT"
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_NO_ACTION:
		return "NO ACTION"
	default:
		return ""
	}
}

// --- option accessors (safe empty value when the extension or descriptor is absent) ---

func ormDatasourceOpts(d protoreflect.FileDescriptor) *ormpbv1.DatasourceOptions {
	if d == nil || !proto.HasExtension(d.Options(), ormpbv1.E_Datasource) {
		return &ormpbv1.DatasourceOptions{}
	}
	return proto.GetExtension(d.Options(), ormpbv1.E_Datasource).(*ormpbv1.DatasourceOptions)
}

func ormTableOpts(d protoreflect.MessageDescriptor) *ormpbv1.TableOptions {
	if d == nil || !proto.HasExtension(d.Options(), ormpbv1.E_Table) {
		return &ormpbv1.TableOptions{}
	}
	return proto.GetExtension(d.Options(), ormpbv1.E_Table).(*ormpbv1.TableOptions)
}

func ormColumnOpts(d protoreflect.FieldDescriptor) *ormpbv1.ColumnOptions {
	if d == nil || !proto.HasExtension(d.Options(), ormpbv1.E_Column) {
		return &ormpbv1.ColumnOptions{}
	}
	return proto.GetExtension(d.Options(), ormpbv1.E_Column).(*ormpbv1.ColumnOptions)
}

func ormQueryOpts(d protoreflect.FieldDescriptor) *ormpbv1.QueryOptions {
	if d == nil || !proto.HasExtension(d.Options(), ormpbv1.E_Query) {
		return &ormpbv1.QueryOptions{}
	}
	return proto.GetExtension(d.Options(), ormpbv1.E_Query).(*ormpbv1.QueryOptions)
}
