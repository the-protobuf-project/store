package resources

// view.go turns one database's IR into the template data for its descriptor
// file. Every mapping decision from schema.Table to store.Resource is made here.

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
	"github.com/the-protobuf-project/store/plugin/factory/provenance"
	"github.com/the-protobuf-project/store/plugin/factory/target/types"
)

// protoModule is the import path of the proto runtime, needed for the
// New func() proto.Message constructor on every descriptor.
const protoModule = "google.golang.org/protobuf/proto"

// database is the rendered view of one database's descriptors.
type database struct {
	Header    string
	Package   string
	Imports   []importLine
	Resources []resource
	Skipped   []string
}

// importLine is one import in the generated file. Alias is empty when the
// package's own name is already unambiguous.
type importLine struct {
	Alias string
	Path  string
}

// resource is the rendered view of one store.Resource literal.
type resource struct {
	Comment       string
	Name          string
	Schema        string
	Table         string
	PKColumn      string
	NewExpr       string // e.g. "&bookstorev1.Book{}"
	SchemaVersion string
	Columns       []column
	FKs           []foreignKey
}

// column is the rendered view of one store.Column literal.
type column struct {
	Comment    string
	Name       string
	Field      string // proto field name; empty for synthesized columns
	Kind       string // store.Kind constant identifier
	SQLType    string
	PrimaryKey bool
	NotNull    bool
	Unique     bool
	Generated  string
	AutoCreate bool
	AutoUpdate bool
}

// foreignKey is the rendered view of one store.ForeignKey literal.
type foreignKey struct {
	Column          string
	ReferencedName  string
	ReferencedField string
}

// data renders the view as the map the template consumes.
func (d database) data() map[string]any {
	return map[string]any{
		"Header":    d.Header,
		"Package":   d.Package,
		"Imports":   d.Imports,
		"Resources": d.Resources,
		"Skipped":   d.Skipped,
	}
}

// databaseView builds the descriptor view for one database.
func databaseView(db *schema.Database, idx *pbIndex, fx facets.Set) (database, error) {
	imp := newImports()
	imp.add(storeModule, "store")
	imp.add(protoModule, "proto")

	var (
		out     []resource
		skipped []string
	)
	for _, s := range db.Schemas {
		for _, t := range s.Tables {
			msg, ok := idx.lookup(t.ProtoMessage)
			if !ok {
				// A synthesized table (many-to-many join) maps to no proto
				// message, so there is nothing for New to construct. See the
				// package doc for why these are dropped rather than emitted
				// with a nil constructor.
				skipped = append(skipped, skipLabel(s, t))
				continue
			}
			r, err := resourceView(db, s, t, msg, imp, fx)
			if err != nil {
				return database{}, err
			}
			out = append(out, r)
		}
	}

	return database{
		Header: provenance.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Database:      db.Name,
			SchemaLabel:   "package",
			Schema:        pkgName,
			Notes: []string{
				"Backend-agnostic resource descriptors: one store.Resource per proto resource.",
				"Register them with store.NewRegistry(Resources...) and pass any driver.",
			},
		}, storeModule),
		Package:   pkgName,
		Imports:   imp.lines(),
		Resources: out,
		Skipped:   skipped,
	}, nil
}

// resourceView maps one table onto a store.Resource literal.
func resourceView(db *schema.Database, s *schema.Schema, t *schema.Table, msg *protogen.Message, imp *imports, fx facets.Set) (resource, error) {
	pkg := imp.add(string(msg.GoIdent.GoImportPath), goPackageName(string(msg.GoIdent.GoImportPath)))

	cols := make([]column, 0, len(t.Columns))
	for _, c := range t.Columns {
		cols = append(cols, columnView(s, c, fx))
	}

	fks := make([]foreignKey, 0, len(t.ForeignKeys))
	for _, fk := range t.ForeignKeys {
		fks = append(fks, foreignKey{
			Column: fk.Column,
			// Resolve through the database rather than trusting fk.ReferencedModel:
			// Name below is Table.ModelName, which is qualified on a cross-schema
			// collision, and a registry lookup that disagreed with the key it was
			// registered under would fail only at runtime.
			ReferencedName:  referencedName(db, fk),
			ReferencedField: fk.ReferencedColumn,
		})
	}

	return resource{
		Comment: t.Comment,
		// ModelName, not LocalName: the registry is one namespace per database,
		// and ModelName is the form protokit qualifies on a cross-schema
		// collision so it stays unique within it.
		Name:          t.ModelName,
		Schema:        s.Name,
		Table:         t.Name,
		PKColumn:      t.PKColumn,
		NewExpr:       fmt.Sprintf("&%s.%s{}", pkg, msg.GoIdent.GoName),
		SchemaVersion: t.SchemaVersion,
		Columns:       cols,
		FKs:           fks,
	}, nil
}

// columnView maps one column onto a store.Column literal.
func columnView(s *schema.Schema, c *schema.Column, fx facets.Set) column {
	return column{
		Comment: c.Comment,
		Name:    c.Name,
		// Field is how the bridge reaches the value on the proto message.
		// Synthesized columns (the surrogate key, audit timestamps) map to no
		// field; they are Managed() at runtime and the driver fills them, so an
		// empty Field is correct rather than missing.
		Field:      protoFieldName(c),
		Kind:       storeKind(c.Type),
		SQLType:    sqlType(s, c, fx),
		PrimaryKey: c.PrimaryKey,
		NotNull:    c.NotNull,
		Unique:     c.Unique,
		Generated:  c.Generated,
		AutoCreate: c.AutoCreate,
		AutoUpdate: c.AutoUpdate,
	}
}

// sqlType resolves the backend column type a relational driver reads off the
// descriptor.
//
// Enum columns need the same treatment the SQL target gives them: the neutral
// projection has no type for TypeEnum, because an enum column's type is the
// schema-qualified PostgreSQL type that CREATE TYPE declares. Leaving it empty
// would hand the relational driver a column it cannot write, so this mirrors
// sql.colDef rather than letting the gap through.
func sqlType(s *schema.Schema, c *schema.Column, fx facets.Set) string {
	if c.Enum != nil {
		return quoteIdent(s.Name) + "." + quoteIdent(c.Enum.LocalSQLName)
	}
	return types.SQLForColumn(c, fx.Column(c))
}

// quoteIdent wraps a PostgreSQL identifier in double quotes, doubling any
// embedded quote.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// protoFieldName returns the proto field a column was built from, or "" for a
// column protokit synthesized.
func protoFieldName(c *schema.Column) string {
	if c.Source == nil {
		return ""
	}
	return string(c.Source.Name())
}

// referencedName resolves a foreign key's target to the ModelName the referenced
// table is registered under, falling back to the FK's own model name when the
// target is not in this database (a cross-database reference).
func referencedName(db *schema.Database, fk *schema.ForeignKey) string {
	for _, s := range db.Schemas {
		if fk.ReferencedSchema != "" && s.Name != fk.ReferencedSchema {
			continue
		}
		for _, t := range s.Tables {
			if t.Name == fk.ReferencedTable {
				return t.ModelName
			}
		}
	}
	return fk.ReferencedModel
}

// skipLabel names a dropped table for the generated file's note.
func skipLabel(s *schema.Schema, t *schema.Table) string {
	return s.Name + "." + t.Name
}

// pbIndex resolves proto message full names to protogen messages so the
// generated descriptors name the exact generated Go types.
type pbIndex struct {
	msgs map[protoreflect.FullName]*protogen.Message
}

func newPbIndex(p *protogen.Plugin) *pbIndex {
	idx := &pbIndex{msgs: map[protoreflect.FullName]*protogen.Message{}}
	var walk func(msgs []*protogen.Message)
	walk = func(msgs []*protogen.Message) {
		for _, m := range msgs {
			idx.msgs[m.Desc.FullName()] = m
			walk(m.Messages)
		}
	}
	for _, f := range p.Files {
		walk(f.Messages)
	}
	return idx
}

// lookup returns the protogen message for a fully qualified proto message name.
func (i *pbIndex) lookup(name string) (*protogen.Message, bool) {
	if name == "" {
		return nil, false
	}
	m, ok := i.msgs[protoreflect.FullName(name)]
	return m, ok
}

// imports collects the generated file's imports, assigning a unique local name
// to each path.
type imports struct {
	byPath map[string]string
	used   map[string]bool
}

func newImports() *imports {
	return &imports{byPath: map[string]string{}, used: map[string]bool{}}
}

// add registers path under a name derived from want, returning the local name to
// qualify references with. Calling it twice with the same path is idempotent.
func (im *imports) add(path, want string) string {
	if name, ok := im.byPath[path]; ok {
		return name
	}
	name := want
	for n := 2; im.used[name]; n++ {
		name = fmt.Sprintf("%s%d", want, n)
	}
	im.byPath[path] = name
	im.used[name] = true
	return name
}

// lines returns the imports sorted by path, aliased only where the local name
// differs from the package's own base name.
func (im *imports) lines() []importLine {
	out := make([]importLine, 0, len(im.byPath))
	for path, name := range im.byPath {
		alias := name
		if goPackageName(path) == name {
			alias = ""
		}
		out = append(out, importLine{Alias: alias, Path: path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// goPackageName derives the default local name for an import path: its last
// segment with anything but letters and digits removed, so "gen/bookstore/v1"
// yields "v1" and a path ending in a dotted segment stays a valid identifier.
func goPackageName(path string) string {
	base := path
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "pb"
	}
	return b.String()
}
