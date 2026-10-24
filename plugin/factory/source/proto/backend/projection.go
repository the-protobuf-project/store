// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package backend

// projection.go synthesizes the table (store.v1.projection) asks for. As with
// the outbox, the trick is that the projection becomes an ordinary
// schema.Table during enrichment: the GORM model, the SQL DDL, the Prisma
// model, the migration registry and the filter spec all appear from the
// renderers every other table goes through. What is projection-specific --
// the range column's type and the GiST/GIN indexes -- rides on facets the
// renderers already read (a column type override, the searchindex plan).
//
// The message is not a resource, so the table carries no Source: the
// repository and converter emitters, which work from resources, skip it, and
// the repository target emits the projection's own code instead (see
// target/repository/projection.go).

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
	"github.com/the-protobuf-project/store/plugin/pb/storepbv1"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ProjectionKeyColumn is the projection table's primary key: the row's key
// fields, JSON-encoded as an array, so one column identifies a row on every
// backend (a GraphQL delete-by-key needs a single-column key).
const ProjectionKeyColumn = "id"

// ProjectionVersionColumn is the projection table's row version: every upsert
// of a row sets it higher than the stored one (a microsecond clock, bumped
// past the old value under the row lock), so a copy of the table elsewhere --
// the OpenSearch index -- can apply changes with external versioning and
// never let an older row overwrite a newer one.
const ProjectionVersionColumn = "row_version"

// appendProjectionTables adds the table of every projection the run declares
// to the database and schema it belongs in.
func appendProjectionTables(ir *schema.IR, fx facets.Set) error {
	for _, p := range fx.Projections() {
		if err := appendProjection(ir, p); err != nil {
			return fmt.Errorf("store.v1.projection on %s: %w", p.Message.FullName(), err)
		}
	}
	return nil
}

func appendProjection(ir *schema.IR, p *facets.Projection) error {
	o, msg := p.Options, p.Message
	if len(o.GetKey()) == 0 {
		return fmt.Errorf("key is required")
	}
	_, s, src, err := projectionHome(ir, p)
	if err != nil {
		return err
	}
	name := o.GetTable()
	if name == "" {
		name = naming.SnakePlural(string(msg.Name()))
	}
	for _, t := range s.Tables {
		if t.Name == name {
			return fmt.Errorf("table %q.%q already exists", s.Name, name)
		}
	}
	path := msg.ParentFile().Path()
	t := &schema.Table{
		Name:         name,
		LocalName:    string(msg.Name()),
		ModelName:    string(msg.Name()),
		Comment:      leadingComment(msg.ParentFile(), msg),
		PKColumn:     ProjectionKeyColumn,
		PgSchema:     s.Name,
		SourceFile:   strings.TrimSuffix(path[strings.LastIndexByte(path, '/')+1:], ".proto"),
		SourceProto:  path,
		ProtoMessage: string(msg.FullName()),
		// Node, not Source: facets.Set.Projection finds the options by it, and
		// a nil Source keeps the resource-driven emitters away.
		Node: schema.NodeIDOfMessage(msg),
	}
	if src != nil {
		t.SourceDir = src.SourceDir
	}
	if t.Comment == "" {
		t.Comment = "Projection of " + strings.Join(o.GetSources(), ", ") + "."
	}
	t.Columns = append(t.Columns, &schema.Column{
		Name:       ProjectionKeyColumn,
		Comment:    "The row's key: its key fields' values as a JSON array.",
		Type:       schema.TypeText,
		NotNull:    true,
		PrimaryKey: true,
	})
	keys := map[string]bool{}
	for _, k := range o.GetKey() {
		keys[k] = true
	}
	rangeField := o.GetRangeField()
	fields := msg.Fields()
	for _, k := range o.GetKey() {
		if fields.ByName(protoreflect.Name(k)) == nil {
			return fmt.Errorf("key names %q, which %s does not have", k, msg.Name())
		}
	}
	if rangeField != "" && fields.ByName(protoreflect.Name(rangeField)) == nil {
		return fmt.Errorf("range_field names %q, which %s does not have", rangeField, msg.Name())
	}
	byName := map[string]string{}
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		c, err := projectionColumn(ir, fd, keys[string(fd.Name())], string(fd.Name()) == rangeField)
		if err != nil {
			return err
		}
		if c.Name == ProjectionKeyColumn || c.Name == ProjectionVersionColumn {
			return fmt.Errorf("field %q collides with the generated column %q", fd.Name(), c.Name)
		}
		c.Comment = leadingComment(msg.ParentFile(), fd)
		t.Columns = append(t.Columns, c)
		byName[string(fd.Name())] = c.Name
	}
	t.Columns = append(t.Columns, &schema.Column{
		Name:    ProjectionVersionColumn,
		Comment: "The row's version: raised by every upsert of the row.",
		Type:    schema.TypeInt64,
		NotNull: true,
		Default: "0",
	})
	var keyCols []string
	for _, k := range o.GetKey() {
		keyCols = append(keyCols, byName[k])
	}
	t.Indexes = append(t.Indexes, &schema.Index{Columns: keyCols, Unique: true})
	for _, idx := range o.GetIndexes() {
		if idx.GetExpression() != "" || isSearchMethod(idx.GetMethod()) {
			continue // planned by searchindex, which carries access methods
		}
		var cols []string
		for _, f := range idx.GetFields() {
			col, ok := byName[f]
			if !ok {
				return fmt.Errorf("index names %q, which %s does not have", f, msg.Name())
			}
			cols = append(cols, col)
		}
		if len(cols) == 0 {
			return fmt.Errorf("an index needs fields or an expression")
		}
		t.Indexes = append(t.Indexes, &schema.Index{Name: idx.GetIndex(), Columns: cols, Unique: idx.GetUnique()})
	}
	s.Tables = append(s.Tables, t)
	return nil
}

// isSearchMethod reports whether m is an access method schema.Index cannot
// carry.
func isSearchMethod(m storepbv1.IndexMethod) bool {
	return m == storepbv1.IndexMethod_INDEX_METHOD_GIN || m == storepbv1.IndexMethod_INDEX_METHOD_GIST
}

// projectionHome finds the database and schema a projection's table belongs
// in: the database holding a table of the projection's own proto file, else
// one of its sources; the schema the option names, else the first source's,
// else the same file's. src is the table the choice came from, if any.
func projectionHome(ir *schema.IR, p *facets.Projection) (*schema.Database, *schema.Schema, *schema.Table, error) {
	path := p.Message.ParentFile().Path()
	type hit struct {
		db *schema.Database
		s  *schema.Schema
		t  *schema.Table
	}
	var sameFile, source *hit
	for _, db := range ir.Databases {
		for _, s := range db.Schemas {
			for _, t := range s.Tables {
				if sameFile == nil && t.SourceProto == path {
					sameFile = &hit{db, s, t}
				}
				if source == nil && t.Source != nil {
					rd, _ := proto.GetExtension(t.Source.Options(), annotations.E_Resource).(*annotations.ResourceDescriptor)
					for _, typ := range p.Options.GetSources() {
						if rd.GetType() == typ {
							source = &hit{db, s, t}
						}
					}
				}
			}
		}
	}
	home := source
	if sameFile != nil {
		home = sameFile
		if source != nil && source.db == sameFile.db {
			home = source
		}
	}
	if home == nil {
		if len(ir.Databases) != 1 || len(ir.Databases[0].Schemas) == 0 {
			return nil, nil, nil, fmt.Errorf("no source and no table of %s is generated in this run, so its database is unknown", path)
		}
		home = &hit{db: ir.Databases[0], s: ir.Databases[0].Schemas[0]}
	}
	s := home.s
	if name := p.Options.GetSchema(); name != "" {
		s = nil
		for _, x := range home.db.Schemas {
			if x.Name == name {
				s = x
			}
		}
		if s == nil {
			s = &schema.Schema{Name: name}
			home.db.Schemas = append(home.db.Schemas, s)
		}
	}
	return home.db, s, home.t, nil
}

// projectionColumn maps one field of a projection message to its column. The
// mapping is deliberately small -- scalars, enums (stored as their value
// name), timestamps, dates, JSON documents, repeated scalars, and the range --
// because a projection is a flat search row: anything structured belongs in
// the JSON attributes.
func projectionColumn(ir *schema.IR, fd protoreflect.FieldDescriptor, key, isRange bool) (*schema.Column, error) {
	c := &schema.Column{
		Name:   string(fd.Name()),
		Source: fd,
		Node:   schema.NodeIDOfField(fd),
		List:   fd.IsList(),
	}
	if isRange {
		if fd.Message() == nil || fd.Message().FullName() != "google.type.Interval" {
			return nil, fmt.Errorf("range_field %q must be a google.type.Interval (repeated for a multirange)", fd.Name())
		}
		typ := "TSTZRANGE"
		if fd.IsList() {
			typ = "TSTZMULTIRANGE"
		}
		c.Type, c.List, c.NotNull = schema.TypeInterval, false, true
		pinColumnType(ir, c.Node, typ)
		return c, nil
	}
	if fd.IsMap() {
		c.Type, c.List = schema.TypeJSON, false
		return c, nil
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		// Keys and names outgrow VARCHAR(255) easily, and a search row has no
		// use for the limit; a repeated string is TEXT[].
		c.Type = schema.TypeText
	case protoreflect.EnumKind:
		c.Type = schema.TypeString
	case protoreflect.BoolKind:
		c.Type = schema.TypeBool
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		c.Type = schema.TypeInt32
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		c.Type = schema.TypeUint32
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		c.Type = schema.TypeInt64
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		c.Type = schema.TypeUint64
	case protoreflect.FloatKind:
		c.Type = schema.TypeFloat
	case protoreflect.DoubleKind:
		c.Type = schema.TypeDouble
	case protoreflect.MessageKind:
		switch fd.Message().FullName() {
		case "google.protobuf.Timestamp":
			c.Type = schema.TypeTimestamp
		case "google.type.Date":
			c.Type = schema.TypeDate
		case "google.protobuf.Struct", "google.protobuf.Value", "google.protobuf.ListValue":
			c.Type, c.List = schema.TypeJSON, false
		default:
			return nil, fmt.Errorf("field %q: a projection column cannot be a %s (use scalars, Timestamp, Date, Struct, or the range Interval)", fd.Name(), fd.Message().FullName())
		}
	default:
		return nil, fmt.Errorf("field %q: unsupported kind %s for a projection column", fd.Name(), fd.Kind())
	}
	if c.List && c.Type != schema.TypeString && c.Type != schema.TypeText {
		if c.Type == schema.TypeTimestamp || c.Type == schema.TypeDate {
			return nil, fmt.Errorf("field %q: a projection cannot store a repeated %s", fd.Name(), fd.Message().FullName())
		}
	}
	switch {
	case key:
		c.NotNull = true
	case fd.Kind() == protoreflect.MessageKind && c.Type != schema.TypeJSON:
		c.Optional = true // an unset Timestamp/Date is NULL
	case fd.HasPresence() && fd.Kind() != protoreflect.MessageKind:
		c.Optional = true // proto3 optional
	}
	return c, nil
}

// pinColumnType gives a synthesized column a store.v1.column type override, so
// every target resolves it through the facet as it would an annotated field.
// An explicit (store.v1.column).type on the field wins.
func pinColumnType(ir *schema.IR, node schema.NodeID, typ string) {
	if ir.Facets == nil {
		ir.Facets = map[string]map[schema.NodeID]any{}
	}
	byNode := ir.Facets[facets.KeyStore]
	if byNode == nil {
		byNode = map[schema.NodeID]any{}
		ir.Facets[facets.KeyStore] = byNode
	}
	f, _ := byNode[node].(*facets.Field)
	if f == nil {
		f = &facets.Field{}
		byNode[node] = f
	}
	if f.Column == nil {
		f.Column = &storepbv1.ColumnOptions{}
	}
	if f.Column.GetType() == "" {
		f.Column.Type = typ
	}
}

// leadingComment is a descriptor's leading comment, trimmed to one paragraph
// of plain text.
func leadingComment(f protoreflect.FileDescriptor, d protoreflect.Descriptor) string {
	loc := f.SourceLocations().ByDescriptor(d)
	return strings.Join(strings.Fields(loc.LeadingComments), " ")
}
