// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// graphql_view.go prepares the per-schema graphql.go / graphql_convert.go
// views: adapters implementing the repository interfaces over the generated
// GraphQL client (by naming convention — pgSchema+table derive the client's
// package and field names), plus the proto↔row/input converters the graphql
// side previously never had. Fragments are rendered here; templates splice.

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
)

// gqlResourceView is the graphql-adapter view of one resource.
type gqlResourceView struct {
	gormResourceView

	Domain      string // client domain field, e.g. "Organisation"
	Resource    string // client resource field, e.g. "Members"
	ResPkg      string // client resource package name, e.g. "membersql"
	Row         string // qualified row type, e.g. "membersql.OrganisationMembers"
	ParentPred  string // parent scope predicate field, e.g. "membersql.OrganisationId"
	EtagPred    string // etag predicate, e.g. "membersql.Etag" (HasEtag only)
	LowerModelV string // camelCase model for converter func names

	ParentInputField string // CreateInput parent FK field, e.g. "OrganisationId"
	EtagInputField   string // CreateInput/UpdateInput etag field ("" = none)
	CreateTimeField  string // CreateInput create-time field ("" = none)
	UpdateTimeField  string // CreateInput/UpdateInput update-time field ("" = none)

	// Rendered fragments.
	InputAssigns []string // CreateInput field assignments from the incoming proto
	PatchAssigns []string // UpdateInput (Nullable) assignments from the merged proto
	RowToProto   []string // proto field assignments from the row
	NeedsHelpers helperNeeds

	// Value-object fragments (see vo_gql_view.go). Named apart from the
	// embedded gorm fragments, which speak tx/gorm and must not leak here.
	GQLVOCreates        []string // insert-then-reference before the resource insert
	GQLVOHydrates       []string // follow-up reads inside toProto
	GQLVOStaleVars      []string // stale-id declarations inside Update
	GQLVOUpdates        []string // masked replacements on the patch
	GQLVOStaleDels      []string // stale-row deletions after a successful update
	GQLVODeleteCleanups []string // VO-row removals after the owner row's delete
}

// helperNeeds tracks which scalar helpers graphql_convert.go must include.
// Wrappers marks google.protobuf wrapper fields (wrapperspb import).
type helperNeeds struct {
	Ts, Date, Struct, Bytes, Duration, StrSlice, Decimal, Enum, Wrappers bool
}

func (h *helperNeeds) or(o helperNeeds) {
	h.Ts = h.Ts || o.Ts
	h.Date = h.Date || o.Date
	h.Struct = h.Struct || o.Struct
	h.Bytes = h.Bytes || o.Bytes
	h.Duration = h.Duration || o.Duration
	h.StrSlice = h.StrSlice || o.StrSlice
	h.Decimal = h.Decimal || o.Decimal
	h.Enum = h.Enum || o.Enum
	h.Wrappers = h.Wrappers || o.Wrappers
}

// export uppercases the first letter of each underscore part without Go
// initialism normalization — the generated GraphQL client's field spelling
// (Id, Uri), matching its own export() rule.
func export(name string) string {
	parts := strings.Split(name, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}

// gqlResourceViews builds the graphql adapter views, parallel to gormViews.
func gqlResourceViews(pb *pbIndex, db *schema.Database, s *schema.Schema, resources map[*schema.Table]*resource, gormViews []gormResourceView) ([]gqlResourceView, helperNeeds, error) {
	rs := sortedResources(s, resources)
	var needs helperNeeds
	out := make([]gqlResourceView, 0, len(rs))
	for i, r := range rs {
		domain := naming.PascalGo(s.Name)
		model := clientModelName(s, r.Table)
		rest := strings.TrimPrefix(model, domain)
		if rest == "" {
			rest = model
		}
		v := gqlResourceView{
			gormResourceView: gormViews[i],
			Domain:           domain,
			Resource:         rest,
			ResPkg:           clientPkgIdent(rest),
			Row:              clientPkgIdent(rest) + "." + model,
			LowerModelV:      naming.CamelFirst(r.Table.LocalName),
		}
		if v.Parented {
			v.ParentPred = v.ResPkg + "." + export(camel(r.ParentFK))
			v.ParentInputField = export(camel(r.ParentFK))
			v.GQLParentAssigns = nil // rebuilt for the client input spelling
			for _, a := range r.AncestorFKs {
				v.GQLParentAssigns = append(v.GQLParentAssigns,
					fmt.Sprintf("ci.%s = parentIDs[%d]", export(camel(a.Column)), a.Index))
			}
		}
		if v.HasEtag {
			v.EtagPred = v.ResPkg + ".Etag"
			v.EtagInputField = export(camel(r.Cols.Etag.Name))
		}
		for _, c := range r.Cols.Autos {
			if c.AutoCreate {
				v.CreateTimeField = export(camel(c.Name))
			}
			if c.AutoUpdate {
				v.UpdateTimeField = export(camel(c.Name))
			}
		}
		if v.CreateTimeField != "" || v.UpdateTimeField != "" {
			needs.Ts = true
		}
		var rn helperNeeds
		v.InputAssigns, v.PatchAssigns, v.RowToProto, rn = gqlColumnFragments(pb, db, resources, r)
		v.NeedsHelpers = rn
		needs.or(rn)
		vp := gqlVOFragments(pb, s, r)
		v.GQLVOCreates = vp.Creates
		v.GQLVOHydrates = vp.Hydrates
		v.GQLVOStaleVars = vp.StaleVars
		v.GQLVOUpdates = vp.Updates
		v.GQLVOStaleDels = vp.StaleDels
		v.GQLVODeleteCleanups = vp.DeleteCleanups
		out = append(out, v)
	}
	return out, needs, nil
}

// clientModelName is the DDN model name the graphql client uses for a table:
// Pascal(schema) + Pascal(table).
func clientModelName(s *schema.Schema, t *schema.Table) string {
	return naming.PascalGo(s.Name) + naming.PascalGo(t.Name)
}

// clientPkgIdent lowercases a resource field into its client package name
// ("UnitLicences" → "unitlicencesql"), mirroring the graphql target's
// identifier() rule (alnum only).
func clientPkgIdent(rest string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(rest) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String() + "ql"
}

// camel converts snake_case to camelCase ("display_name" → "displayName"),
// the form DDN exposes columns as.
func camel(snake string) string {
	parts := strings.Split(snake, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

// gqlColumnFragments renders, for every column the flat adapters own, the
// CreateInput assignment, the UpdateInput (replace-write) assignment, and the
// row→proto read-back. Reference columns store bare ids and are decorated with
// the target's collection prefix on the way out (root-resource refs only, like
// the gorm side). Unsupported scalar shapes are skipped with a marker comment.
func gqlColumnFragments(pb *pbIndex, db *schema.Database, resources map[*schema.Table]*resource, r *resource) (inputs, patches, rows []string, needs helperNeeds) {
	addRow := func(line string) { rows = append(rows, line) }

	// name: stored verbatim, read back verbatim.
	if r.Cols.Name != nil {
		inputs = append(inputs, "ci.Name = in.GetName()")
		addRow("out.Name = row.Name")
	}

	for _, c := range r.Cols.Mutable {
		rowField := "row." + export(camel(c.Name))
		if c.FKModel != "" {
			target := findResourceByModel(db, resources, c.FKModel)
			if target == nil || len(target.Segments) != 1 {
				continue // nested-resource reference: Tier-2
			}
			collection := target.Segments[0].Collection
			inField := export(camel(c.Name)) // e.g. UserId (bare-id column)
			acc := "in.Get" + pbGoField(pb, c) + "()"
			mAcc := "merged.Get" + pbGoField(pb, c) + "()"
			outField := "out." + pbGoField(pb, c)
			inputs = append(inputs, fmt.Sprintf("if v := %s; v != \"\" {\n\t\tci.%s = repox.LastSegment(v)\n\t}", acc, inField))
			patches = append(patches, gqlPatch(inField, "repox.LastSegment("+mAcc+")", `""`, c.Optional))
			if c.Optional {
				addRow(fmt.Sprintf("if v := repox.Deref(%s); v != \"\" {\n\t\t%s = %q + v\n\t}", rowField, outField, collection+"/"))
			} else {
				addRow(fmt.Sprintf("if %s != \"\" {\n\t\t%s = %q + %s\n\t}", rowField, outField, collection+"/", rowField))
			}
			continue
		}
		in, patch, row, n, ok := gqlScalarFragments(pb, c, rowField)
		if !ok {
			inputs = append(inputs, "// not mapped here: "+c.Name+" (unsupported scalar shape)")
			continue
		}
		needs.or(n)
		inputs = append(inputs, in...)
		patches = append(patches, patch...)
		rows = append(rows, row...)
	}

	// etag + audit timestamps + stored OUTPUT_ONLY columns read back.
	if r.Cols.Etag != nil {
		addRow("out.Etag = repox.Deref(row.Etag)")
	}
	for _, c := range r.Cols.Autos {
		if c.Source == nil {
			continue
		}
		acc := "out." + pbGoField(pb, c)
		f := "row." + export(camel(c.Name))
		if c.Optional {
			addRow(fmt.Sprintf("%s = strToTs(repox.Deref(%s))", acc, f))
		} else {
			addRow(fmt.Sprintf("%s = strToTs(%s)", acc, f))
		}
		needs.Ts = true
	}
	for _, c := range r.Cols.Skipped {
		if c.Source == nil || c.FKModel != "" || !outputOnly(c) {
			continue
		}
		// Stored OUTPUT_ONLY columns (e.g. a state enum) still render out.
		if c.Type == schema.TypeEnum && c.Enum != nil {
			prefix := naming.ScreamingSnake(c.Enum.LocalName) + "_"
			pbT, ok := pbEnumType(pb, c)
			if !ok {
				continue
			}
			f := "row." + export(camel(c.Name))
			v := f
			if c.Optional {
				v = "repox.Deref(" + f + ")"
			}
			addRow(fmt.Sprintf("out.%s = %s(%s_value[%q+%s])", pbGoField(pb, c), pbT, pbT, prefix, v))
		}
	}
	return inputs, patches, rows, needs
}

// gqlPatch renders one UpdateInput assignment with replace-write semantics:
// optional columns null out when the merged value is zero, mirroring the gorm
// write-back's pointer-nil behavior.
func gqlPatch(field, expr, zero string, optional bool) string {
	if optional {
		return fmt.Sprintf("if v := %s; v != %s {\n\t\tpatch.%s = graphql.Value(v)\n\t} else {\n\t\tpatch.%s = graphql.Null[%s]()\n\t}",
			expr, zero, field, field, patchType(zero))
	}
	return fmt.Sprintf("patch.%s = graphql.Value(%s)", field, expr)
}

// patchType maps a zero literal onto the Nullable type parameter.
func patchType(zero string) string {
	switch zero {
	case "0":
		return "int32"
	case "graphql.Int64(0)":
		return "graphql.Int64"
	case `graphql.Bigdecimal("")`:
		return "graphql.Bigdecimal"
	case "0.0":
		return "float64"
	case "false":
		return "bool"
	default:
		return "string"
	}
}

// pbEnumType resolves the qualified generated Go enum type for a column
// ("orgpbv1.OrganisationState") via protogen.
func pbEnumType(pb *pbIndex, c *schema.Column) (string, bool) {
	if c.Source == nil {
		return "", false
	}
	m, ok := pb.msgs[c.Source.ContainingMessage().FullName()]
	if !ok {
		return "", false
	}
	for _, f := range m.Fields {
		if f.Desc.FullName() == c.Source.FullName() && f.Enum != nil {
			return pb.names.Of(string(f.Enum.GoIdent.GoImportPath)) + "." + f.Enum.GoIdent.GoName, true
		}
	}
	return "", false
}

// gqlOneofOut returns the row-assignment target for a column whose proto field
// is a oneof member: the oneof struct field, and the wrapper type the value is
// boxed in. ok is false for a plain field.
//
// protoc-gen-go gives a oneof member no struct field of its own — the value
// lives behind a generated wrapper on the oneof field — so "out.Duration = v"
// does not compile. This mirrors what the gorm converter does; the two paths
// hit the same protos and must agree.
func gqlOneofOut(pb *pbIndex, c *schema.Column) (oneofField, wrapper string, ok bool) {
	f := pbFieldOf(pb, c)
	if f == nil || f.Oneof == nil || f.Oneof.Desc.IsSynthetic() {
		return "", "", false
	}
	return f.Oneof.GoName, pb.names.Of(string(f.GoIdent.GoImportPath)) + "." + f.GoIdent.GoName, true
}

// gqlScalarFragments renders the three fragments for one plain scalar column.
func gqlScalarFragments(pb *pbIndex, c *schema.Column, rowField string) (inputs, patches, rows []string, needs helperNeeds, ok bool) {
	field := export(camel(c.Name))
	gof := pbGoField(pb, c)
	acc := "in.Get" + gof + "()"
	mAcc := "merged.Get" + gof + "()"
	outField := "out." + gof
	// assignRow renders the row->proto assignment. A oneof member has no struct
	// field of its own, so it is boxed in its wrapper and guarded: an absent
	// column must leave the oneof unset rather than select an arm holding nil.
	assignRow := func(expr string) string {
		oneof, wrapper, isOneof := gqlOneofOut(pb, c)
		if !isOneof {
			return fmt.Sprintf("%s = %s", outField, expr)
		}
		return fmt.Sprintf("if v := %s; v != nil {\n\t\tout.%s = &%s{%s: v}\n\t}", expr, oneof, wrapper, gof)
	}
	rowVal := rowField
	if c.Optional {
		rowVal = "repox.Deref(" + rowField + ")"
	}

	// google.protobuf wrapper fields: nil-able scalars on both sides.
	if f := pbFieldOf(pb, c); f != nil && f.Message != nil && !c.List {
		if in2, p2, r2, handled := gqlWrapperFragments(string(f.Message.Desc.FullName()), field, gof, rowField); handled {
			if !c.Optional {
				return nil, nil, nil, needs, false // a NOT NULL wrapper column loses absence: Tier-2
			}
			needs.Wrappers = true
			return in2, p2, r2, needs, true
		}
	}
	set := func(inExpr, patchExpr, rowExpr, zero string) {
		inputs = append(inputs, fmt.Sprintf("ci.%s = %s", field, inExpr))
		patches = append(patches, gqlPatch(field, patchExpr, zero, c.Optional))
		rows = append(rows, assignRow(rowExpr))
	}

	if c.List {
		if c.Type == schema.TypeString {
			needs.StrSlice = true
			// The client represents text[] with nullable elements ([]*string).
			inputs = append(inputs, fmt.Sprintf("ci.%s = strSliceToPtrs(%s)", field, acc))
			patches = append(patches, fmt.Sprintf("patch.%s = graphql.Value(strSliceToPtrs(%s))", field, mAcc))
			rows = append(rows, assignRow("strPtrsToSlice("+rowField+")"))
			return inputs, patches, rows, needs, true
		}
		return nil, nil, nil, needs, false
	}

	switch c.Type {
	case schema.TypeString, schema.TypeText:
		set(acc, mAcc, rowVal, `""`)
	case schema.TypeEnum:
		if c.Enum == nil {
			return nil, nil, nil, needs, false
		}
		needs.Enum = true
		prefix := naming.ScreamingSnake(c.Enum.LocalName) + "_"
		pbT, okT := pbEnumType(pb, c)
		if !okT {
			return nil, nil, nil, needs, false
		}
		toStr := func(a string) string {
			return fmt.Sprintf("strings.TrimPrefix(%s.String(), %q)", a, prefix)
		}
		inputs = append(inputs, fmt.Sprintf("if v := %s; v != 0 {\n\t\tci.%s = %s\n\t}", acc, field, toStr(acc)))
		patches = append(patches, gqlPatch(field, toStr(mAcc), fmt.Sprintf("%q", "UNSPECIFIED"), c.Optional))
		rows = append(rows, assignRow(fmt.Sprintf("%s(%s_value[%q+%s])", pbT, pbT, prefix, rowVal)))
	case schema.TypeInt32, schema.TypeUint32:
		set(fmt.Sprintf("int32(%s)", acc), fmt.Sprintf("int32(%s)", mAcc), fmt.Sprintf("%s(%s)", pbCast(pb, c), rowVal), "0")
	case schema.TypeInt64, schema.TypeUint64:
		inputs = append(inputs, fmt.Sprintf("ci.%s = graphql.Int64(%s)", field, acc))
		patches = append(patches, gqlPatch(field, fmt.Sprintf("graphql.Int64(%s)", mAcc), "graphql.Int64(0)", c.Optional))
		rows = append(rows, assignRow(fmt.Sprintf("%s(%s)", pbCast(pb, c), rowVal)))
	case schema.TypeFloat, schema.TypeDouble:
		set(acc, mAcc, rowVal, "0.0")
	case schema.TypeDecimal:
		needs.Decimal = true
		inputs = append(inputs, fmt.Sprintf("ci.%s = bigdec(%s)", field, acc))
		patches = append(patches, gqlPatch(field, fmt.Sprintf("bigdec(%s)", mAcc), `graphql.Bigdecimal("")`, c.Optional))
		rows = append(rows, assignRow(fmt.Sprintf("fromBigdec(%s)", rowVal)))
	case schema.TypeBool:
		set(acc, mAcc, rowVal, "false")
	case schema.TypeTimestamp:
		// protokit maps more than one well-known type onto this schema type
		// (TypeTimestamp covers google.type.DateTime too), and they are different
		// Go types. Emitting the helper for the wrong one does not compile, so an
		// unrecognised message is skipped as an unsupported shape — the same guard
		// the gorm converter applies.
		if f := pbFieldOf(pb, c); f != nil && f.Message != nil && string(f.Message.Desc.FullName()) != "google.protobuf.Timestamp" {
			return nil, nil, nil, needs, false
		}
		needs.Ts = true
		inputs = append(inputs, fmt.Sprintf("ci.%s = tsToStr(%s)", field, acc))
		patches = append(patches, gqlPatch(field, fmt.Sprintf("tsToStr(%s)", mAcc), `""`, c.Optional))
		rows = append(rows, assignRow(fmt.Sprintf("strToTs(%s)", rowVal)))
	case schema.TypeDate:
		// protokit maps more than one well-known type onto this schema type
		// (TypeTimestamp covers google.type.DateTime too), and they are different
		// Go types. Emitting the helper for the wrong one does not compile, so an
		// unrecognised message is skipped as an unsupported shape — the same guard
		// the gorm converter applies.
		if f := pbFieldOf(pb, c); f != nil && f.Message != nil && string(f.Message.Desc.FullName()) != "google.type.Date" {
			return nil, nil, nil, needs, false
		}
		needs.Date = true
		inputs = append(inputs, fmt.Sprintf("if v := dateToStr(%s); v != \"\" {\n\t\tci.%s = v\n\t}", acc, field))
		patches = append(patches, gqlPatch(field, fmt.Sprintf("dateToStr(%s)", mAcc), `""`, c.Optional))
		rows = append(rows, assignRow(fmt.Sprintf("strToDate(%s)", rowVal)))
	case schema.TypeDuration:
		// protokit maps more than one well-known type onto this schema type
		// (TypeTimestamp covers google.type.DateTime too), and they are different
		// Go types. Emitting the helper for the wrong one does not compile, so an
		// unrecognised message is skipped as an unsupported shape — the same guard
		// the gorm converter applies.
		if f := pbFieldOf(pb, c); f != nil && f.Message != nil && string(f.Message.Desc.FullName()) != "google.protobuf.Duration" {
			return nil, nil, nil, needs, false
		}
		needs.Duration = true
		inputs = append(inputs, fmt.Sprintf("if v := durToStr(%s); v != \"\" {\n\t\tci.%s = v\n\t}", acc, field))
		patches = append(patches, gqlPatch(field, fmt.Sprintf("durToStr(%s)", mAcc), `""`, c.Optional))
		rows = append(rows, assignRow(fmt.Sprintf("strToDur(%s)", rowVal)))
	case schema.TypeJSON:
		// TypeJSON also covers a proto map field, which is a distinct Go type
		// (map[string]*T, not *structpb.Struct). Same guard as above.
		if f := pbFieldOf(pb, c); f != nil && f.Message != nil && string(f.Message.Desc.FullName()) != "google.protobuf.Struct" {
			return nil, nil, nil, needs, false
		}
		if f := pbFieldOf(pb, c); f != nil && f.Desc.IsMap() {
			return nil, nil, nil, needs, false
		}
		needs.Struct = true
		inputs = append(inputs, fmt.Sprintf("ci.%s = structToJSON(%s)", field, acc))
		patches = append(patches, fmt.Sprintf("patch.%s = graphql.Value(structToJSON(%s))", field, mAcc))
		rows = append(rows, assignRow(fmt.Sprintf("jsonToStruct(%s)", rowField)))
	case schema.TypeBytes:
		needs.Bytes = true
		inputs = append(inputs, fmt.Sprintf("ci.%s = bytesToRaw(%s)", field, acc))
		patches = append(patches, fmt.Sprintf("patch.%s = graphql.Value(bytesToRaw(%s))", field, mAcc))
		rows = append(rows, assignRow(fmt.Sprintf("rawToBytes(%s)", rowField)))
	default:
		return nil, nil, nil, needs, false
	}
	return inputs, patches, rows, needs, true
}

// pbFieldOf resolves the protogen field backing a column ("" for synthesized
// columns), so fragment builders can inspect the proto-side shape.
func pbFieldOf(pb *pbIndex, c *schema.Column) *protogen.Field {
	if c.Source == nil {
		return nil
	}
	m, ok := pb.msgs[c.Source.ContainingMessage().FullName()]
	if !ok {
		return nil
	}
	for _, f := range m.Fields {
		if f.Desc.FullName() == c.Source.FullName() {
			return f
		}
	}
	return nil
}

// gqlWrapperFragments renders the three fragments for a google.protobuf
// wrapper field over a nullable column: set only when the wrapper is present,
// null the patch when the merged wrapper is absent, and rebuild the wrapper
// from the row pointer.
func gqlWrapperFragments(wrapperName, field, gof, rowField string) (inputs, patches, rows []string, handled bool) {
	acc := "in.Get" + gof + "()"
	mAcc := "merged.Get" + gof + "()"
	outField := "out." + gof
	rf := "*" + rowField
	var inConv, nullT, rowExpr string
	switch wrapperName {
	case "google.protobuf.Int64Value":
		inConv, nullT = "graphql.Int64(v.GetValue())", "graphql.Int64"
		rowExpr = "wrapperspb.Int64(int64(" + rf + "))"
	case "google.protobuf.UInt64Value":
		inConv, nullT = "graphql.Int64(v.GetValue())", "graphql.Int64"
		rowExpr = "wrapperspb.UInt64(uint64(" + rf + "))"
	case "google.protobuf.Int32Value":
		inConv, nullT = "int32(v.GetValue())", "int32"
		rowExpr = "wrapperspb.Int32(int32(" + rf + "))"
	case "google.protobuf.UInt32Value":
		inConv, nullT = "int32(v.GetValue())", "int32"
		rowExpr = "wrapperspb.UInt32(uint32(" + rf + "))"
	case "google.protobuf.StringValue":
		inConv, nullT = "v.GetValue()", "string"
		rowExpr = "wrapperspb.String(" + rf + ")"
	case "google.protobuf.BoolValue":
		inConv, nullT = "v.GetValue()", "bool"
		rowExpr = "wrapperspb.Bool(" + rf + ")"
	case "google.protobuf.FloatValue":
		inConv, nullT = "float64(v.GetValue())", "float64"
		rowExpr = "wrapperspb.Float(float32(" + rf + "))"
	case "google.protobuf.DoubleValue":
		inConv, nullT = "v.GetValue()", "float64"
		rowExpr = "wrapperspb.Double(" + rf + ")"
	default:
		return nil, nil, nil, false
	}
	inputs = append(inputs, fmt.Sprintf("if v := %s; v != nil {\n\t\tci.%s = %s\n\t}", acc, field, inConv))
	patches = append(patches, fmt.Sprintf(
		"if v := %s; v != nil {\n\t\tpatch.%s = graphql.Value(%s)\n\t} else {\n\t\tpatch.%s = graphql.Null[%s]()\n\t}",
		mAcc, field, inConv, field, nullT))
	rows = append(rows, fmt.Sprintf("if %s != nil {\n\t\t%s = %s\n\t}", rowField, outField, rowExpr))
	return inputs, patches, rows, true
}

// pbCast is the proto-side numeric type for read-back casts (int32 vs uint32
// etc.), resolved from protogen when available.
func pbCast(pb *pbIndex, c *schema.Column) string {
	switch c.Type {
	case schema.TypeUint32:
		return "uint32"
	case schema.TypeInt64:
		return "int64"
	case schema.TypeUint64:
		return "uint64"
	default:
		return "int32"
	}
}
