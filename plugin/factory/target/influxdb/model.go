// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package influxdb

// model.go validates one time_series message against what a point can hold and
// maps its fields to Rust types, runtime kinds and conversions.

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/entity/pb/entitypbv1"
	"github.com/the-protobuf-project/store/plugin/pb/storepbv1"
)

// timestampProto is the one message type a measurement field may have.
const timestampProto = "google.protobuf.Timestamp"

// modelView is one measurement: its struct, descriptor and Model impl in
// models.rs, and its store in <model>_store.rs.
type modelView struct {
	Name      string // Rust struct, the proto message simple name
	Static    string // the descriptor's static, SCREAMING_SNAKE
	Table     string // the table the backends store its points in
	Proto     string // fully-qualified proto message
	Comment   string
	Fields    []fieldView
	Store     string // the store type, <Name>Store
	StoreFile string // its file and module, <name>_store
	TagEnum   string // <Name>Tag, the tags a query filters and groups by
	FieldEnum string // <Name>Field, the numeric fields it reduces
	Time      string // the time column
	Tags      []columnView
	Numbers   []columnView
}

type fieldView struct {
	Ident   string // Rust field identifier, raw when a keyword
	Column  string // stored column name
	Type    string // Rust type
	Kind    string // database::store::Kind variant
	Role    string // database::store::Role variant
	Encode  string // expression yielding the column's Value
	Decode  string // expression rebuilding the field from `record`
	Comment string

	numeric bool                        // a value an aggregate can reduce
	enum    protoreflect.EnumDescriptor // the proto enum an Enum field stores
}

// columnView is one variant of a store's Tag or Field enum.
type columnView struct {
	Column  string
	Variant string
	Method  string // the query's filter method, for a tag
	Comment string
}

// queryMethods are seriesx::Query's own methods, which a tag's filter method
// cannot share a name with.
var queryMethods = map[string]bool{
	"new": true, "between": true, "since": true, "until": true, "eq": true, "filter": true,
	"newest_first": true, "page_size": true, "page_token": true, "group_by": true,
	"fetch": true, "all": true, "latest": true, "count": true, "aggregate": true,
}

// enumView is a proto enum a model field stores, rendered as a Rust enum
// beside the models. Fields keep the i32, as prost does, so a number this
// build does not know still round-trips; the enum converts with TryFrom.
type enumView struct {
	Name     string
	Proto    string
	Comment  string
	Variants []variantView
}

type variantView struct {
	Name    string
	Number  int32
	Comment string
}

// modelOf validates t against the shape a point can take and builds its view.
func modelOf(t *schema.Table, ts *storepbv1.TimeSeries) (*modelView, error) {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("influxdb: %s: "+format, append([]any{t.ProtoMessage}, args...)...)
	}
	if t.Source == nil {
		return nil, fail("time_series on a table built from no message")
	}

	// Only the message's own fields are rendered: a surrogate key, audit
	// timestamps and embed foreign keys are relational structure the other
	// targets synthesize, not values a point carries.
	byField := map[protoreflect.Name]*schema.Column{}
	for _, c := range t.Columns {
		if c.Source != nil && c.Source.ContainingMessage() == t.Source {
			byField[c.Source.Name()] = c
		}
	}
	fields := t.Source.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if byField[fd.Name()] == nil && !skipped(fd) {
			return nil, fail("field %q becomes no column of its own (a message-typed field is a child table); "+
				"measurement fields must be scalars or google.protobuf.Timestamp", fd.Name())
		}
	}

	timeCol := byField[protoreflect.Name(ts.GetTime())]
	if ts.GetTime() == "" || timeCol == nil {
		return nil, fail("time_series.time %q names no field of the message", ts.GetTime())
	}
	if !isTimestamp(timeCol) && (timeCol.Type != schema.TypeInt64 || timeCol.Source.Kind() == protoreflect.MessageKind) {
		return nil, fail("time_series.time %q must be a google.protobuf.Timestamp or an int64 of epoch nanoseconds", ts.GetTime())
	}
	tags := map[protoreflect.Name]bool{}
	for _, tag := range ts.GetTags() {
		name := protoreflect.Name(tag)
		c := byField[name]
		switch {
		case c == nil:
			return nil, fail("time_series.tags %q names no field of the message", tag)
		case c == timeCol:
			return nil, fail("time_series.tags %q is the time field", tag)
		case tags[name]:
			return nil, fail("time_series.tags lists %q twice", tag)
		case c.Type != schema.TypeString || c.Source.Kind() != protoreflect.StringKind:
			return nil, fail("time_series.tags %q must be a string field", tag)
		}
		tags[name] = true
	}

	m := &modelView{
		Name:      t.LocalName,
		Static:    naming.ScreamingSnake(t.LocalName),
		Table:     t.Name,
		Proto:     t.ProtoMessage,
		Comment:   t.Comment,
		Store:     t.LocalName + "Store",
		StoreFile: naming.SnakeCase(t.LocalName) + "_store",
		TagEnum:   t.LocalName + "Tag",
		FieldEnum: t.LocalName + "Field",
		Time:      timeCol.Name,
	}
	values := 0
	for _, c := range t.Columns {
		if c.Source == nil || byField[c.Source.Name()] != c {
			continue
		}
		role := "Field"
		switch {
		case c == timeCol:
			role = "Timestamp"
		case tags[c.Source.Name()]:
			role = "Tag"
		default:
			values++
		}
		f, err := fieldOf(c, role)
		if err != nil {
			return nil, fail("field %q: %v", c.Source.Name(), err)
		}
		m.Fields = append(m.Fields, f)
		col := columnView{Column: c.Name, Variant: variantName(c.Name), Comment: c.Comment}
		switch {
		case role == "Tag":
			col.Method = ident(c.Name)
			if queryMethods[col.Method] {
				col.Method += "_tag"
			}
			m.Tags = append(m.Tags, col)
		case role == "Field" && f.numeric:
			m.Numbers = append(m.Numbers, col)
		}
	}
	if values == 0 {
		return nil, fail("a measurement needs at least one field that is neither the time nor a tag")
	}
	return m, nil
}

// fieldOf maps one column to its Rust type, runtime Kind and conversions.
func fieldOf(c *schema.Column, role string) (fieldView, error) {
	fd := c.Source
	switch {
	case c.List:
		return fieldView{}, fmt.Errorf("repeated fields cannot be stored in a point")
	case c.Document:
		return fieldView{}, fmt.Errorf("documents cannot be stored in a point")
	case fd.ContainingOneof() != nil && !fd.ContainingOneof().IsSynthetic():
		return fieldView{}, fmt.Errorf("oneof members cannot be stored in a point: an unset member would be a null")
	case fd.Kind() == protoreflect.MessageKind && fd.Message().FullName() != timestampProto:
		return fieldView{}, fmt.Errorf("%s cannot be stored in a point; use a scalar or google.protobuf.Timestamp", fd.Message().FullName())
	case fd.Kind() != protoreflect.MessageKind && fd.HasPresence():
		return fieldView{}, fmt.Errorf("explicit presence (optional) cannot be stored in a point: neither backend stores a null")
	}
	v := fieldView{Ident: ident(c.Name), Column: c.Name, Role: role, Comment: c.Comment}
	self := "self." + v.Ident
	decode := func(fn string) string { return fmt.Sprintf("decode::%s(&mut record, %q)?", fn, c.Name) }
	switch c.Type {
	case schema.TypeString:
		v.Type, v.Kind, v.Encode, v.Decode = "String", "String", self+".as_str().into()", decode("string")
	case schema.TypeBool:
		v.Type, v.Kind, v.Encode, v.Decode = "bool", "Bool", self+".into()", decode("boolean")
	case schema.TypeInt32:
		v.Type, v.Kind, v.Encode, v.Decode, v.numeric = "i32", "Int", self+".into()", decode("int"), true
	case schema.TypeInt64:
		v.Type, v.Kind, v.Encode, v.Decode, v.numeric = "i64", "Int", self+".into()", decode("int"), true
	case schema.TypeUint32:
		v.Type, v.Kind, v.Encode, v.Decode, v.numeric = "u32", "Uint", self+".into()", decode("uint"), true
	case schema.TypeUint64:
		v.Type, v.Kind, v.Encode, v.Decode, v.numeric = "u64", "Uint", self+".into()", decode("uint"), true
	case schema.TypeFloat:
		v.Type, v.Kind, v.Encode, v.Decode, v.numeric = "f32", "Float", self+".into()", decode("float")+" as f32", true
	case schema.TypeDouble:
		v.Type, v.Kind, v.Encode, v.Decode, v.numeric = "f64", "Float", self+".into()", decode("float"), true
	case schema.TypeEnum:
		v.Type, v.Kind, v.Encode, v.Decode = "i32", "Enum", self+".into()", decode("int")
		v.enum = fd.Enum()
	case schema.TypeTimestamp:
		// The runtime keeps Kind::Timestamp for the one column that orders the
		// series; any other instant is a value, stored as its nanoseconds.
		v.Type, v.Kind, v.Encode, v.Decode = "i64", "Int", self+".into()", decode("int")
		if role == "Timestamp" {
			v.Kind = "Timestamp"
		}
		v.Comment = joinComment(v.Comment, "Nanoseconds since the Unix epoch.", "nanosecond")
	default:
		return fieldView{}, fmt.Errorf("%s fields have no tpp-database kind", fd.Kind())
	}
	if role == "Timestamp" && c.Type == schema.TypeInt64 {
		v.Kind = "Timestamp"
		v.Comment = joinComment(v.Comment, "Nanoseconds since the Unix epoch.", "nanosecond")
	}
	return v, nil
}

// enumOf renders ed under a name no model in its module takes. Variants drop
// the AIP-126 prefix (POINT_QUALITY_GOOD → Good); an alias of an earlier
// number is left out, since a Rust discriminant cannot repeat.
func enumOf(ed protoreflect.EnumDescriptor, names map[string]bool) *enumView {
	e := &enumView{
		Name:    naming.Unique(string(ed.Name()), names),
		Proto:   string(ed.FullName()),
		Comment: comment(ed),
	}
	prefix := naming.ScreamingSnake(string(ed.Name())) + "_"
	seen := map[int32]bool{}
	used := map[string]bool{}
	values := ed.Values()
	for i := range values.Len() {
		v := values.Get(i)
		if seen[int32(v.Number())] {
			continue
		}
		seen[int32(v.Number())] = true
		e.Variants = append(e.Variants, variantView{
			Name:    naming.Unique(variantName(strings.TrimPrefix(string(v.Name()), prefix)), used),
			Number:  int32(v.Number()),
			Comment: comment(v),
		})
	}
	return e
}

// variantName is the PascalCase form of a SCREAMING_SNAKE value name.
func variantName(s string) string {
	var b strings.Builder
	for _, part := range strings.Split(strings.ToLower(s), "_") {
		if part != "" {
			b.WriteString(strings.ToUpper(part[:1]) + part[1:])
		}
	}
	name := b.String()
	switch {
	case name == "":
		return "Value"
	case name[0] >= '0' && name[0] <= '9':
		return "V" + name
	case name == "Self":
		return "Self_"
	}
	return name
}

// comment is d's leading proto comment as one line.
func comment(d protoreflect.Descriptor) string {
	loc := d.ParentFile().SourceLocations().ByDescriptor(d)
	return strings.Join(strings.Fields(loc.LeadingComments), " ")
}

func isTimestamp(c *schema.Column) bool {
	return c.Type == schema.TypeTimestamp && c.Source.Kind() == protoreflect.MessageKind &&
		c.Source.Message().FullName() == timestampProto
}

// skipped reports (entity.v1.column).skip, the one reason a field has no column
// that is not an error here.
func skipped(fd protoreflect.FieldDescriptor) bool {
	o, _ := proto.GetExtension(fd.Options(), entitypbv1.E_Column).(*entitypbv1.ColumnOptions)
	return o.GetSkip()
}

// joinComment appends note b to comment a, unless a already mentions key.
func joinComment(a, b, key string) string {
	if a == "" {
		return b
	}
	if strings.Contains(strings.ToLower(a), key) {
		return a
	}
	return strings.TrimRight(a, ".") + ". " + b
}

// ident makes name a Rust identifier: a keyword becomes a raw identifier, and
// the four that cannot be raw take a trailing underscore.
func ident(name string) string {
	switch name {
	case "self", "Self", "super", "crate":
		return name + "_"
	}
	if rustKeywords[name] {
		return "r#" + name
	}
	return name
}

// moduleName is the module, and the file it lives in, for schema s. A file
// cannot be named r#type.rs, so a keyword takes the trailing underscore.
func moduleName(s string) string {
	name := naming.SnakeCase(s)
	if ident(name) != name {
		return strings.TrimSuffix(name, "_") + "_"
	}
	return name
}

var rustKeywords = map[string]bool{
	"as": true, "async": true, "await": true, "break": true, "const": true, "continue": true,
	"dyn": true, "else": true, "enum": true, "extern": true, "false": true, "fn": true, "for": true,
	"gen": true, "if": true, "impl": true, "in": true, "let": true, "loop": true, "match": true,
	"mod": true, "move": true, "mut": true, "pub": true, "ref": true, "return": true, "static": true,
	"struct": true, "trait": true, "true": true, "type": true, "unsafe": true, "use": true,
	"where": true, "while": true, "abstract": true, "become": true, "box": true, "do": true,
	"final": true, "macro": true, "override": true, "priv": true, "try": true, "typeof": true,
	"unsized": true, "virtual": true, "yield": true,
}
