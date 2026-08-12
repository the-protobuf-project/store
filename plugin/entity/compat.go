package entity

// compat.go reads the structural options that entity.v1 replaced — the pre-split
// orm.v1 vocabulary, and the structural half of store.v1 before it was reduced to
// physical storage — and maps them onto the same neutral structs reader.go
// produces, so protos written against the old options keep generating while their
// authors migrate.
//
// # Why this reads descriptors instead of importing stubs
//
// The obvious implementation imports ormpbv1 and storepbv1 and calls
// proto.GetExtension. This package cannot do that. orm.v1 settles it outright: the
// vocabulary was removed from the repository entirely, so there are no stubs to
// import at any version. storepbv1 is a softer case now that both live in the same
// module, but the import stays out anyway — this package is the neutral half, and
// keeping it free of the physical vocabulary is what lets it move back into a
// module of its own without an untangling job first.
//
// So the deprecated extensions are resolved from the descriptor set protoc already
// shipped. A proto that sets (orm.v1.table) must import orm/v1/annotations.proto,
// which means the extension's *declaration* is in the request's transitive
// imports, whether or not any Go package for it was ever linked into this binary.
// dynamicpb turns that declaration into an extension type, the options are
// re-parsed through it, and the fields are read by name.
//
// Reading by name rather than by field number is what makes one reader cover both
// vocabularies. orm.v1.table.timestamps and store.v1.table.timestamps were the
// same option under two package names; a vocabulary that never had a given field
// simply yields nothing for it, with no per-vocabulary table to keep in sync.

import (
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/schema"
)

// CompatKey is the facet key the compatibility reader is registered under.
//
// It sorts after [Key], which is the whole point: where a node carries both an
// entity.v1 annotation and a legacy one, entity.v1 is resolved first and wins, and
// protokit reports the legacy value as a lint diagnostic naming both rather than
// dropping it silently.
const CompatKey = "entity.v1-compat"

// deprecatedFile, deprecatedMessage, and deprecatedField are the extension names
// this reader looks for, in precedence order within the compat layer itself.
// orm.v1 comes first because it is strictly older: a proto carrying both is
// mid-migration, and the newer spelling should win the tie.
var (
	deprecatedFile    = []protoreflect.FullName{"orm.v1.datasource", "store.v1.datasource"}
	deprecatedMessage = []protoreflect.FullName{"orm.v1.table", "store.v1.table"}
	deprecatedField   = []protoreflect.FullName{"orm.v1.column", "store.v1.column"}
)

// CompatReader returns the reader for the deprecated structural vocabularies.
//
// Register it alongside [Reader]. It contributes nothing for a proto that uses
// only entity.v1, so leaving it registered costs a map lookup per node and buys
// every pre-migration proto in the wild.
func CompatReader() protokit.FacetReader { return &compat{} }

// compat caches the per-file extension resolution, which is the expensive half:
// building it walks a file's whole transitive import closure, and a build asks for
// the same file's options once per message and once per field in it.
type compat struct{ types sync.Map } // file path -> *protoregistry.Types

// Compile-time proof that compat satisfies all three halves of the seam.
var (
	_ schema.FacetReader         = (*compat)(nil)
	_ schema.StructureReader     = (*compat)(nil)
	_ schema.DeprecatedStructure = (*compat)(nil)
)

// Key namespaces this reader's facets.
func (*compat) Key() string { return CompatKey }

// StructureDeprecation is the clause protokit appends to each lint diagnostic.
// protokit names the vocabulary and the option it saw; naming the replacement is
// this reader's job, because protokit owns no annotation module and knows of none.
func (*compat) StructureDeprecation() string {
	return "these are read for compatibility only; replace them with the matching (entity.v1.*) option"
}

// --- schema.FacetReader ---
//
// A compatibility shim contributes no facets: everything it reads is structure
// protokit acts on during the build, and a target reading a deprecated value back
// at render time is a migration going the wrong way.

func (*compat) ReadFile(protoreflect.FileDescriptor) (any, error)       { return nil, nil }
func (*compat) ReadMessage(protoreflect.MessageDescriptor) (any, error) { return nil, nil }
func (*compat) ReadField(protoreflect.FieldDescriptor) (any, error)     { return nil, nil }

// --- schema.StructureReader ---

// ReadDatasource maps a deprecated file-level datasource option onto the neutral
// grouping.
func (c *compat) ReadDatasource(d protoreflect.FileDescriptor) schema.Datasource {
	m := c.lookup(d, d, deprecatedFile)
	if m == nil {
		return schema.Datasource{}
	}
	return schema.Datasource{
		Database: stringField(m, "database"),
		Schema:   stringField(m, "schema"),
		URL:      stringField(m, "url"),
		Provider: stringField(m, "provider"),
	}
}

// ReadTable maps a deprecated message-level table option onto the neutral table
// structure.
func (c *compat) ReadTable(d protoreflect.MessageDescriptor) schema.TableStructure {
	if d == nil {
		return schema.TableStructure{}
	}
	m := c.lookup(d.ParentFile(), d, deprecatedMessage)
	if m == nil {
		return schema.TableStructure{}
	}
	return schema.TableStructure{
		Table:      stringField(m, "table"),
		Skip:       boolField(m, "skip"),
		ID:         compatIDStrategy(m),
		Timestamps: boolField(m, "timestamps"),
		Indexes:    compatIndexes(m),
	}
}

// ReadColumn maps a deprecated field-level column option onto the neutral column
// structure.
//
// Only the name and skip are read. Referential actions are not deprecated — they
// are the permanent, non-neutral half a generator's own vocabulary supplies — so
// store.v1's live reader still owns them and this one must not double-report them
// as a migration.
func (c *compat) ReadColumn(d protoreflect.FieldDescriptor) schema.ColumnStructure {
	if d == nil {
		return schema.ColumnStructure{}
	}
	m := c.lookup(d.ParentFile(), d, deprecatedField)
	if m == nil {
		return schema.ColumnStructure{}
	}
	return schema.ColumnStructure{
		Column: stringField(m, "column"),
		Skip:   boolField(m, "skip"),
	}
}

// --- dynamic option resolution ---

// optioned is the part of a descriptor this reader needs: its options message.
// All of FileDescriptor, MessageDescriptor, and FieldDescriptor satisfy it.
type optioned interface {
	Options() proto.Message
}

// lookup returns the first of names that d carries as an extension on its options,
// re-parsed so its fields can be read even when no Go package for the extension
// was linked into this binary. Returns nil when the descriptor carries none of
// them, which is the common case.
//
// file is the descriptor's own file: the root of the import closure the extension
// declaration must be found in.
func (c *compat) lookup(file protoreflect.FileDescriptor, d optioned, names []protoreflect.FullName) protoreflect.Message {
	if file == nil || d == nil {
		return nil
	}
	opts := d.Options()
	if opts == nil || !opts.ProtoReflect().IsValid() {
		return nil
	}

	types := c.typesFor(file)
	// No deprecated extension is even declared in this file's imports, so nothing
	// on it can be one. This is the fast path for every proto already migrated.
	present := false
	for _, n := range names {
		if _, err := types.FindExtensionByName(n); err == nil {
			present = true
			break
		}
	}
	if !present {
		return nil
	}

	reparsed := reparse(opts, types)
	if reparsed == nil {
		return nil
	}
	for _, n := range names {
		xt, err := types.FindExtensionByName(n)
		if err != nil {
			continue
		}
		xd := xt.TypeDescriptor()
		if xd.Kind() != protoreflect.MessageKind || !reparsed.Has(xd) {
			continue
		}
		return reparsed.Get(xd).Message()
	}
	return nil
}

// typesFor returns the extension types declared anywhere in file's transitive
// imports, built once per file.
func (c *compat) typesFor(file protoreflect.FileDescriptor) *protoregistry.Types {
	if v, ok := c.types.Load(file.Path()); ok {
		return v.(*protoregistry.Types)
	}
	types := new(protoregistry.Types)
	seen := map[string]bool{}
	var walk func(f protoreflect.FileDescriptor)
	walk = func(f protoreflect.FileDescriptor) {
		if f == nil || seen[f.Path()] {
			return
		}
		seen[f.Path()] = true
		// Top-level extensions only: an annotation module declares its extends at
		// file scope, and a nested one is not a vocabulary anyone imports.
		exts := f.Extensions()
		for i := range exts.Len() {
			// A duplicate registration means two files in the closure declare the
			// same extension, which protoc would already have rejected; ignore the
			// error rather than fail a build over an impossible case.
			_ = types.RegisterExtension(dynamicpb.NewExtensionType(exts.Get(i)))
		}
		imps := f.Imports()
		for i := range imps.Len() {
			walk(imps.Get(i).FileDescriptor)
		}
	}
	walk(file)

	actual, _ := c.types.LoadOrStore(file.Path(), types)
	return actual.(*protoregistry.Types)
}

// reparse re-decodes an options message through types, so extensions that were
// unknown bytes to the global registry become readable fields.
//
// The round-trip is the mechanism, not an accident: the descriptor's options were
// decoded when protoc's request was unmarshalled, using the global type registry,
// which by definition does not know a vocabulary this binary never linked. Those
// bytes survive as unknown fields. Re-encoding and decoding with a resolver built
// from the request's own descriptors is what turns them back into fields.
func reparse(opts proto.Message, types *protoregistry.Types) protoreflect.Message {
	b, err := proto.Marshal(opts)
	if err != nil {
		return nil
	}
	fresh := opts.ProtoReflect().New()
	if err := (proto.UnmarshalOptions{Resolver: types}).Unmarshal(b, fresh.Interface()); err != nil {
		return nil
	}
	return fresh
}

// compatIDStrategy maps a deprecated id-strategy enum onto the neutral one by
// *value name* rather than number. The two vocabularies numbered their enums
// independently, and a name is what both spellings actually agree on.
func compatIDStrategy(m protoreflect.Message) schema.IDStrategy {
	fd := field(m, "id")
	if fd == nil || fd.Kind() != protoreflect.EnumKind {
		return schema.IDUnspecified
	}
	ev := fd.Enum().Values().ByNumber(m.Get(fd).Enum())
	if ev == nil {
		return schema.IDUnspecified
	}
	switch {
	case strings.HasSuffix(string(ev.Name()), "ULID"):
		return schema.IDULID
	case strings.HasSuffix(string(ev.Name()), "UUID"):
		return schema.IDUUID
	default:
		return schema.IDUnspecified
	}
}

// compatIndexes converts a deprecated repeated index declaration to the neutral
// IR form, reading each entry's fields by name like everything else here.
func compatIndexes(m protoreflect.Message) []*schema.Index {
	fd := field(m, "indexes")
	if fd == nil || !fd.IsList() || fd.Kind() != protoreflect.MessageKind {
		return nil
	}
	list := m.Get(fd).List()
	if list.Len() == 0 {
		return nil
	}
	out := make([]*schema.Index, 0, list.Len())
	for i := range list.Len() {
		e := list.Get(i).Message()
		out = append(out, &schema.Index{
			Name:    stringField(e, "index"),
			Columns: stringListField(e, "columns"),
			Unique:  boolField(e, "unique"),
		})
	}
	return out
}

// --- field accessors, all tolerant of a vocabulary that never had the field ---

// field returns the named field descriptor, or nil when this vocabulary has no
// such field. Every accessor goes through it, which is what lets one reader cover
// vocabularies with different field sets.
func field(m protoreflect.Message, name string) protoreflect.FieldDescriptor {
	if m == nil {
		return nil
	}
	return m.Descriptor().Fields().ByName(protoreflect.Name(name))
}

func stringField(m protoreflect.Message, name string) string {
	fd := field(m, name)
	if fd == nil || fd.Kind() != protoreflect.StringKind || fd.IsList() {
		return ""
	}
	return m.Get(fd).String()
}

func boolField(m protoreflect.Message, name string) bool {
	fd := field(m, name)
	if fd == nil || fd.Kind() != protoreflect.BoolKind || fd.IsList() {
		return false
	}
	return m.Get(fd).Bool()
}

func stringListField(m protoreflect.Message, name string) []string {
	fd := field(m, name)
	if fd == nil || !fd.IsList() || fd.Kind() != protoreflect.StringKind {
		return nil
	}
	list := m.Get(fd).List()
	out := make([]string, 0, list.Len())
	for i := range list.Len() {
		out = append(out, list.Get(i).String())
	}
	return out
}
