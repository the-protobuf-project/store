package gorm

// protobuf_view.go prepares the protobuf.go template view: for every table
// built from a proto message, a pair of mappers between the proto type and the
// GORM model (<Model>ToProto / <Model>FromProto), plus per-enum value mappers.
// The converters cover the mechanical field mass — scalars, enums, temporals,
// arrays, JSON, and belongs-to value objects on the read side. They do NOT
// invent data or wiring the schema cannot know:
//
//   - synthesized columns (surrogate ids, audit timestamps) are never set from
//     proto input; audit timestamps still render back out;
//   - resource-reference columns (google.api.resource_reference) are skipped in
//     both directions — resource-name↔id mapping stays with the caller;
//   - relationalized sub-rows (value objects) are rendered ToProto from their
//     preloaded associations, but FromProto graph wiring (fresh ids + FK
//     assignment + insert order) stays with the caller, composing each
//     sub-row's own FromProto.
//
// All rendering decisions are made here; protobuf.go.tpl is presentation only.

import (
	"sort"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/orm/plugin/factory/target/types"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
)

// pbIndex resolves proto descriptors to protogen types (Go idents) across every
// file in the codegen request, dependencies included, so converter output can
// reference the exact generated Go types (including google.type WKTs).
type pbIndex struct {
	msgs  map[protoreflect.FullName]*protogen.Message
	enums map[protoreflect.FullName]*protogen.Enum
}

func newPbIndex(p *protogen.Plugin) *pbIndex {
	idx := &pbIndex{
		msgs:  map[protoreflect.FullName]*protogen.Message{},
		enums: map[protoreflect.FullName]*protogen.Enum{},
	}
	var walk func(msgs []*protogen.Message)
	walk = func(msgs []*protogen.Message) {
		for _, m := range msgs {
			idx.msgs[m.Desc.FullName()] = m
			for _, e := range m.Enums {
				idx.enums[e.Desc.FullName()] = e
			}
			walk(m.Messages)
		}
	}
	for _, f := range p.Files {
		walk(f.Messages)
		for _, e := range f.Enums {
			idx.enums[e.Desc.FullName()] = e
		}
	}
	return idx
}

// convImports accumulates the import lines convert.go needs, aliasing a path
// whose package name differs from its last segment.
type convImports struct {
	std   map[string]string // path -> alias ("" = none)
	third map[string]string
}

func newConvImports() *convImports {
	return &convImports{std: map[string]string{}, third: map[string]string{}}
}

func (ci *convImports) addStd(path string) { ci.std[path] = "" }
func (ci *convImports) add(path, pkg string) string {
	alias := ""
	if seg := path[strings.LastIndex(path, "/")+1:]; seg != pkg {
		alias = pkg
	}
	ci.third[path] = alias
	return pkg
}

// render emits the grouped import block (stdlib, then third-party), matching
// the hand-written convention models.go uses.
func (ci *convImports) render() string {
	group := func(m map[string]string) []string {
		paths := make([]string, 0, len(m))
		for p := range m {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		lines := make([]string, 0, len(paths))
		for _, p := range paths {
			if a := m[p]; a != "" {
				lines = append(lines, a+" \""+p+"\"")
			} else {
				lines = append(lines, "\""+p+"\"")
			}
		}
		return lines
	}
	std, third := group(ci.std), group(ci.third)
	if len(std) == 0 && len(third) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("import (\n")
	for _, l := range std {
		b.WriteString("\t" + l + "\n")
	}
	if len(std) > 0 && len(third) > 0 {
		b.WriteByte('\n')
	}
	for _, l := range third {
		b.WriteString("\t" + l + "\n")
	}
	b.WriteString(")")
	return b.String()
}

type convEnumPair struct{ ModelConst, PbConst string }

type convEnum struct {
	LocalName string // model Go enum type
	PbType    string // qualified proto enum type
	Pairs     []convEnumPair
}

type convModel struct {
	Name      string // model LocalName
	PbType    string // qualified proto message type
	ToLines   []string
	FromLines []string
	ToSkip    string // aggregated note of fields ToProto does not map
	FromSkip  string // aggregated note of fields FromProto does not map
}

// helperNeeds tracks which shared helper groups the schema's converters use, so
// convert.go only carries the helpers (and imports) it references. The Val
// variants are the NOT NULL forms of the temporal codecs; EnumCSV is the
// repeated-enum ↔ comma-joined-names codec.
type helperNeeds struct {
	Ptr, Ts, Date, Tod, Dur, JSON    bool
	DateVal, TodVal, DurVal, EnumCSV bool
}

// convertView assembles the template data for one schema's convert.go, or nil
// when no table in the schema maps to a proto message.
func convertView(idx *pbIndex, db *schema.Database, s *schema.Schema, pkg string, typeOf types.TypeOf) (map[string]any, error) {
	imports := newConvImports()
	var needs helperNeeds
	emittedEnums := map[string]string{} // enum ProtoName -> model LocalName

	var enums []convEnum
	for _, e := range s.Enums {
		pbEnum := idx.enums[protoreflect.FullName(e.ProtoName)]
		if pbEnum == nil {
			continue
		}
		parent := fileOf(pbEnum.GoIdent)
		ce := convEnum{
			LocalName: e.LocalName,
			PbType:    imports.add(string(pbEnum.GoIdent.GoImportPath), parent) + "." + pbEnum.GoIdent.GoName,
		}
		for _, ev := range e.Values {
			pv := matchEnumValue(pbEnum, ev)
			if pv == nil {
				continue
			}
			ce.Pairs = append(ce.Pairs, convEnumPair{
				ModelConst: e.LocalName + naming.PascalGo(strings.ToLower(ev.Name)),
				PbConst:    imports.add(string(pv.GoIdent.GoImportPath), parent) + "." + pv.GoIdent.GoName,
			})
		}
		if len(ce.Pairs) > 0 {
			enums = append(enums, ce)
			emittedEnums[e.ProtoName] = e.LocalName
		}
	}

	var models []convModel
	for _, t := range s.Tables {
		if t.Source == nil {
			continue
		}
		msg := idx.msgs[t.Source.FullName()]
		if msg == nil {
			continue
		}
		pbPkg := imports.add(string(msg.GoIdent.GoImportPath), goPackageNameOf(msg))
		cm := convModel{
			Name:   t.LocalName,
			PbType: pbPkg + "." + msg.GoIdent.GoName,
		}
		fieldsByName := map[protoreflect.Name]*protogen.Field{}
		for _, f := range msg.Fields {
			fieldsByName[f.Desc.Name()] = f
		}
		bts, _ := AssocPlan(db, s, t)
		var toSkips, fromSkips []string

		for _, col := range t.Columns {
			if col.Source == nil {
				continue // synthesized (surrogate id, audit column with no proto field)
			}
			f := fieldsByName[col.Source.Name()]
			if f == nil {
				continue
			}
			mField := "m." + gormFieldName(col)
			pField := "out." + f.GoName
			getter := "pb.Get" + f.GoName + "()"

			// Resource references first: their columns are FKs, not data this
			// converter can invent. Relationalized sub-rows (message-typed fields)
			// are handled after the column loop, off the association plan — their
			// FK columns are synthesized and usually carry no Source.
			if col.FKModel != "" {
				if f.Desc.Kind() != protoreflect.MessageKind {
					fromSkips = append(fromSkips, string(col.Source.Name())+" (resource reference)")
					toSkips = append(toSkips, string(col.Source.Name())+" (resource reference)")
				}
				continue
			}

			// A repeated enum is stored as a comma-joined list of proto value
			// names in a nullable text column. Detected off the proto field —
			// the IR may have already flattened the column to a plain string.
			if f.Desc.IsList() && f.Enum != nil {
				if goType(col, typeOf) != "*string" {
					toSkips, fromSkips = skipBoth(toSkips, fromSkips, col)
					continue
				}
				needs.EnumCSV = true
				imports.addStd("fmt")
				imports.addStd("strings")
				pbType := imports.add(string(f.Enum.GoIdent.GoImportPath), fileOf(f.Enum.GoIdent)) + "." + f.Enum.GoIdent.GoName
				cm.FromLines = append(cm.FromLines, mField+" = enumsToCSV("+getter+")")
				cm.ToLines = append(cm.ToLines, pField+" = enumsFromCSV["+pbType+"]("+mField+", "+pbType+"_value)")
				continue
			}

			// Single-valued enums map through the schema-local label mappers.
			if col.Enum != nil {
				local, ok := emittedEnums[col.Enum.ProtoName]
				if !ok {
					toSkips = append(toSkips, string(col.Source.Name())+" (enum)")
					fromSkips = append(fromSkips, string(col.Source.Name())+" (enum)")
					continue
				}
				if col.Optional {
					cm.FromLines = append(cm.FromLines, mField+" = "+local+"PtrFromProto("+getter+")")
					cm.ToLines = append(cm.ToLines, pField+" = "+local+"PtrToProto("+mField+")")
				} else {
					cm.FromLines = append(cm.FromLines, mField+" = "+local+"FromProto("+getter+")")
					cm.ToLines = append(cm.ToLines, pField+" = "+local+"ToProto("+mField+")")
				}
				continue
			}

			// Message-mapped temporals and JSON.
			switch col.Type {
			case schema.TypeTimestamp:
				if fullNameOf(f) != "google.protobuf.Timestamp" {
					toSkips, fromSkips = skipBoth(toSkips, fromSkips, col)
					continue
				}
				needs.Ts = true
				imports.add("google.golang.org/protobuf/types/known/timestamppb", "timestamppb")
				imports.addStd("time")
				if col.AutoCreate || col.AutoUpdate {
					// DB-managed: render out, never set from input.
					cm.ToLines = append(cm.ToLines, pField+" = "+tsOut(col, mField))
					continue
				}
				if col.Optional {
					cm.FromLines = append(cm.FromLines, mField+" = tsToGo("+getter+")")
				} else {
					cm.FromLines = append(cm.FromLines, mField+" = tsToGoVal("+getter+")")
				}
				cm.ToLines = append(cm.ToLines, pField+" = "+tsOut(col, mField))
				continue
			case schema.TypeDate:
				if fullNameOf(f) != "google.type.Date" {
					toSkips, fromSkips = skipBoth(toSkips, fromSkips, col)
					continue
				}
				imports.add("google.golang.org/genproto/googleapis/type/date", "date")
				imports.addStd("time")
				if col.Optional {
					needs.Date = true
					cm.FromLines = append(cm.FromLines, mField+" = dateToGo("+getter+")")
					cm.ToLines = append(cm.ToLines, pField+" = goToDate("+mField+")")
				} else {
					needs.DateVal = true
					cm.FromLines = append(cm.FromLines, mField+" = dateToGoVal("+getter+")")
					cm.ToLines = append(cm.ToLines, pField+" = goValToDate("+mField+")")
				}
				continue
			case schema.TypeTimeOfDay:
				if fullNameOf(f) != "google.type.TimeOfDay" {
					toSkips, fromSkips = skipBoth(toSkips, fromSkips, col)
					continue
				}
				imports.add("google.golang.org/genproto/googleapis/type/timeofday", "timeofday")
				imports.addStd("time")
				if col.Optional {
					needs.Tod = true
					cm.FromLines = append(cm.FromLines, mField+" = todToGo("+getter+")")
					cm.ToLines = append(cm.ToLines, pField+" = goToTod("+mField+")")
				} else {
					needs.TodVal = true
					cm.FromLines = append(cm.FromLines, mField+" = todToGoVal("+getter+")")
					cm.ToLines = append(cm.ToLines, pField+" = goValToTod("+mField+")")
				}
				continue
			case schema.TypeDuration:
				if fullNameOf(f) != "google.protobuf.Duration" {
					toSkips, fromSkips = skipBoth(toSkips, fromSkips, col)
					continue
				}
				imports.add("google.golang.org/protobuf/types/known/durationpb", "durationpb")
				imports.addStd("time")
				if col.Optional {
					needs.Dur = true
					cm.FromLines = append(cm.FromLines, mField+" = durToGo("+getter+")")
					cm.ToLines = append(cm.ToLines, pField+" = goToDur("+mField+")")
				} else {
					needs.DurVal = true
					cm.FromLines = append(cm.FromLines, mField+" = durToGoVal("+getter+")")
					cm.ToLines = append(cm.ToLines, pField+" = goValToDur("+mField+")")
				}
				continue
			case schema.TypeJSON:
				if fullNameOf(f) != "google.protobuf.Struct" {
					toSkips, fromSkips = skipBoth(toSkips, fromSkips, col)
					continue
				}
				needs.JSON = true
				imports.add("google.golang.org/protobuf/types/known/structpb", "structpb")
				imports.addStd("encoding/json")
				cm.FromLines = append(cm.FromLines, mField+" = structToJSON("+getter+")")
				cm.ToLines = append(cm.ToLines, pField+" = jsonToStruct("+mField+")")
				continue
			case schema.TypeText, schema.TypeDecimal, schema.TypeLatLng, schema.TypeInterval:
				toSkips, fromSkips = skipBoth(toSkips, fromSkips, col)
				continue
			}

			// Well-known wrapper types: a nil-able scalar on both sides. The
			// pointer column preserves explicit absence; the value is copied so
			// the row never aliases proto memory.
			if wrapGo, wrapCtor := wrapperScalar(fullNameOf(f)); wrapGo != "" {
				if goType(col, typeOf) != "*"+wrapGo {
					toSkips, fromSkips = skipBoth(toSkips, fromSkips, col)
					continue
				}
				imports.add("google.golang.org/protobuf/types/known/wrapperspb", "wrapperspb")
				cm.FromLines = append(cm.FromLines,
					"if v := "+getter+"; v != nil {",
					"\tval := v.GetValue()",
					"\t"+mField+" = &val",
					"}")
				cm.ToLines = append(cm.ToLines,
					"if "+mField+" != nil {",
					"\t"+pField+" = wrapperspb."+wrapCtor+"(*"+mField+")",
					"}")
				continue
			}

			// Scalars and scalar lists.
			protoGo := protoGoScalar(f.Desc.Kind())
			if protoGo == "" {
				toSkips, fromSkips = skipBoth(toSkips, fromSkips, col)
				continue
			}
			modelType := goType(col, typeOf)
			if f.Desc.HasOptionalKeyword() && f.Desc.Kind() != protoreflect.MessageKind {
				// A proto3 `optional` scalar is a pointer on both sides: copy it
				// through so explicit absence survives (toPtr would erase an
				// explicit zero). Width-converted or non-pointer layouts stay
				// with the caller.
				if modelType != "*"+protoGo {
					toSkips, fromSkips = skipBoth(toSkips, fromSkips, col)
					continue
				}
				cm.FromLines = append(cm.FromLines, mField+" = pb."+f.GoName)
				cm.ToLines = append(cm.ToLines, pField+" = "+mField)
				continue
			}
			if col.List {
				elem, ok := pqElem(modelType)
				if !ok {
					toSkips, fromSkips = skipBoth(toSkips, fromSkips, col)
					continue
				}
				imports.add("github.com/lib/pq", "pq")
				if elem == protoGo {
					cm.FromLines = append(cm.FromLines, mField+" = "+modelType+"("+getter+")")
					cm.ToLines = append(cm.ToLines, pField+" = []"+elem+"("+mField+")")
				} else {
					cm.FromLines = append(cm.FromLines,
						"for _, v := range "+getter+" {",
						"\t"+mField+" = append("+mField+", "+elem+"(v))",
						"}")
					cm.ToLines = append(cm.ToLines,
						"for _, v := range "+mField+" {",
						"\t"+pField+" = append("+pField+", "+protoGo+"(v))",
						"}")
				}
				continue
			}
			base := strings.TrimPrefix(modelType, "*")
			fromExpr := getter
			if base != protoGo {
				fromExpr = base + "(" + getter + ")"
			}
			toExpr := mField
			// A []byte-shaped column (schema.TypeBytes) is already nilable — like
			// goType, which never adds a "*" prefix for it — so it needs no
			// toPtr/fromPtr wrapping; a nil/empty slice already means "unset".
			if col.Optional && !strings.HasPrefix(modelType, "[]") {
				needs.Ptr = true
				fromExpr = "toPtr(" + fromExpr + ")"
				toExpr = "fromPtr(" + mField + ")"
			}
			if base != protoGo {
				toExpr = protoGo + "(" + toExpr + ")"
			}
			toLines := []string{pField + " = " + toExpr}
			if w := oneofWrap(imports, f); w != nil {
				// A oneof member has no direct struct field: assign the arm's wrapper,
				// and only for a non-zero value so unset arms don't clobber the one
				// that is set.
				zero := zeroLit(protoGo)
				if zero == "" {
					toSkips = append(toSkips, string(col.Source.Name())+" (oneof)")
					toLines = nil
				} else {
					toLines = []string{
						"if v := " + toExpr + "; v != " + zero + " {",
						"\tout." + w.oneofField + " = &" + w.wrapper + "{" + f.GoName + ": v}",
						"}",
					}
				}
			}
			if col.PrimaryKey {
				// A proto-sourced primary key renders out but is never set from
				// input — key assignment stays with the caller.
				fromSkips = append(fromSkips, string(col.Source.Name())+" (primary key)")
				cm.ToLines = append(cm.ToLines, toLines...)
				continue
			}
			cm.FromLines = append(cm.FromLines, mField+" = "+fromExpr)
			cm.ToLines = append(cm.ToLines, toLines...)
		}

		// Relationalized sub-rows (value objects): their FK columns are
		// synthesized, so they pair with the proto message field by name
		// (id_document_id → id_document). ToProto renders the preloaded
		// association; FromProto graph wiring (fresh ids, FK assignment, insert
		// order) stays with the caller, composing the sub-row's own FromProto.
		for _, bt := range bts {
			fieldName := naming.StripIDSuffix(bt.Col.Name)
			f := fieldsByName[protoreflect.Name(fieldName)]
			if f == nil || f.Message == nil || bt.Target == nil || bt.Target.Source == nil ||
				string(f.Message.Desc.FullName()) != string(bt.Target.Source.FullName()) {
				continue
			}
			fromSkips = append(fromSkips, fieldName+" (sub-row graph)")
			qual := bt.Target.LocalName + "ToProto"
			if bt.CrossPkg != "" {
				imports.add(dbGoModule(db)+"/"+db.Name+"/"+bt.CrossPkg, bt.CrossPkg)
				qual = bt.CrossPkg + "." + qual
			}
			expr := qual + "(m." + bt.Field + ")"
			if w := oneofWrap(imports, f); w != nil {
				cm.ToLines = append(cm.ToLines,
					"if v := "+expr+"; v != nil {",
					"\tout."+w.oneofField+" = &"+w.wrapper+"{"+f.GoName+": v}",
					"}")
			} else {
				cm.ToLines = append(cm.ToLines, "out."+f.GoName+" = "+expr)
			}
		}

		if len(toSkips) > 0 {
			cm.ToSkip = "// not mapped here: " + strings.Join(toSkips, ", ")
		}
		if len(fromSkips) > 0 {
			cm.FromSkip = "// not mapped here: " + strings.Join(fromSkips, ", ")
		}
		models = append(models, cm)
	}

	if len(models) == 0 && len(enums) == 0 {
		return nil, nil
	}
	return map[string]any{
		"Header": header.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Source:        strings.Join(s.SourceProtos(), ", "),
			Database:      db.Name,
			Schema:        s.Name,
		}),
		"Package": pkg,
		"Imports": imports.render(),
		"Enums":   enums,
		"Models":  models,
		"Needs":   needs,
	}, nil
}

// tsOut renders the ToProto expression for a timestamp column.
func tsOut(col *schema.Column, mField string) string {
	if col.Optional {
		return "goToTs(" + mField + ")"
	}
	return "goValToTs(" + mField + ")"
}

// skipBoth records a column the converters leave to the caller, in both
// directions.
func skipBoth(to, from []string, col *schema.Column) ([]string, []string) {
	n := string(col.Source.Name())
	return append(to, n), append(from, n)
}

// matchEnumValue pairs a protokit enum value with its protogen counterpart by
// recomputing the exact stored MapName protokit derives (the enum-name prefix
// stripped, SCREAMING_SNAKE) — suffix heuristics would mispair values that are
// suffixes of one another (SMOKING vs NON_SMOKING).
func matchEnumValue(pbEnum *protogen.Enum, ev *schema.EnumValue) *protogen.EnumValue {
	enumName := string(pbEnum.Desc.Name())
	for _, pv := range pbEnum.Values {
		mapName := naming.ScreamingSnake(naming.EnumValueName(enumName, string(pv.Desc.Name())))
		if mapName == ev.MapName {
			return pv
		}
	}
	return nil
}

// oneofRef names the pieces a oneof-member assignment needs: the oneof's
// interface-typed struct field and the member's qualified wrapper type.
type oneofRef struct {
	oneofField string
	wrapper    string
}

// oneofWrap returns the wrapper reference for a real (non-synthetic) oneof
// member, or nil for plain fields. Synthetic oneofs (proto3 optional) keep
// their direct struct field.
func oneofWrap(imports *convImports, f *protogen.Field) *oneofRef {
	if f.Oneof == nil || f.Oneof.Desc.IsSynthetic() {
		return nil
	}
	pkg := imports.add(string(f.GoIdent.GoImportPath), fileOf(f.GoIdent))
	return &oneofRef{
		oneofField: f.Oneof.GoName,
		wrapper:    pkg + "." + f.GoIdent.GoName,
	}
}

// zeroLit is the Go zero-value literal for a scalar proto type ("" when the
// type has no usable emptiness check, e.g. bool).
func zeroLit(protoGo string) string {
	switch protoGo {
	case "string":
		return `""`
	case "bool", "[]byte":
		return ""
	default:
		return "0"
	}
}

// wrapperScalar maps a google.protobuf wrapper message name to its Go scalar
// type and the wrapperspb constructor that rebuilds it ("" for non-wrappers).
// BytesValue is excluded: a []byte column is already nil-able.
func wrapperScalar(fullName string) (goType, ctor string) {
	switch fullName {
	case "google.protobuf.Int32Value":
		return "int32", "Int32"
	case "google.protobuf.Int64Value":
		return "int64", "Int64"
	case "google.protobuf.UInt32Value":
		return "uint32", "UInt32"
	case "google.protobuf.UInt64Value":
		return "uint64", "UInt64"
	case "google.protobuf.FloatValue":
		return "float32", "Float"
	case "google.protobuf.DoubleValue":
		return "float64", "Double"
	case "google.protobuf.BoolValue":
		return "bool", "Bool"
	case "google.protobuf.StringValue":
		return "string", "String"
	}
	return "", ""
}

// protoGoScalar maps a scalar proto kind to its generated Go type ("" for
// non-scalar kinds).
func protoGoScalar(k protoreflect.Kind) string {
	switch k {
	case protoreflect.BoolKind:
		return "bool"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return "int32"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return "uint32"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return "int64"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return "uint64"
	case protoreflect.FloatKind:
		return "float32"
	case protoreflect.DoubleKind:
		return "float64"
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BytesKind:
		return "[]byte"
	}
	return ""
}

// pqElem maps a pq array Go type to its element type.
func pqElem(t string) (string, bool) {
	switch t {
	case "pq.StringArray":
		return "string", true
	case "pq.Int64Array":
		return "int64", true
	case "pq.Int32Array":
		return "int32", true
	case "pq.Float64Array":
		return "float64", true
	case "pq.BoolArray":
		return "bool", true
	}
	return "", false
}

// fullNameOf returns the full proto name of a message-typed field's message.
func fullNameOf(f *protogen.Field) string {
	if f.Message == nil {
		return ""
	}
	return string(f.Message.Desc.FullName())
}

// goPackageNameOf returns the Go package name of the file declaring msg.
func goPackageNameOf(msg *protogen.Message) string {
	// GoIdent import paths end in the package directory; the generated package
	// name is the last segment for all supported layouts (pb packages and the
	// genproto WKTs alike).
	p := string(msg.GoIdent.GoImportPath)
	return p[strings.LastIndex(p, "/")+1:]
}

// fileOf mirrors goPackageNameOf for enum idents.
func fileOf(ident protogen.GoIdent) string {
	p := string(ident.GoImportPath)
	return p[strings.LastIndex(p, "/")+1:]
}
