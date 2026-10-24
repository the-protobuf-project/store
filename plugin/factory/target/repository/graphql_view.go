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
	"github.com/the-protobuf-project/store/plugin/factory/target/dtzone"
)

// gqlResourceView is the graphql-adapter view of one resource.
type gqlResourceView struct {
	gormResourceView

	Domain     string // client domain field, e.g. "Organisation"
	Resource   string // client resource field, e.g. "Members"
	ResPkg     string // client resource package name, e.g. "membersql"
	Row        string // qualified row type, e.g. "membersql.OrganisationMembers"
	ParentPred string // parent scope predicate field, e.g. "membersql.OrganisationId"
	EtagPred   string // etag predicate, e.g. "membersql.Etag" (HasEtag only)
	// DeleteTimePred and DeleteTimeInputField address the soft-delete column
	// (SoftDelete only), e.g. "membersql.DeleteTime" and "DeleteTime".
	DeleteTimePred       string
	DeleteTimeInputField string
	ExpireTimeInputField string // AIP-164 expire_time's UpdateInput field (Expiring only)
	LowerModelV          string // camelCase model for converter func names

	ParentInputField string // CreateInput parent FK field, e.g. "OrganisationId"
	EtagInputField   string // CreateInput/UpdateInput etag field ("" = none)
	CreateTimeField  string // CreateInput create-time field ("" = none)
	UpdateTimeField  string // CreateInput/UpdateInput update-time field ("" = none)

	// Rendered fragments.
	InputAssigns []string // CreateInput field assignments from the incoming proto
	PatchAssigns []string // UpdateInput (Nullable) assignments from the merged proto
	// OutputPatchAssigns are UpdateOutputOnly's patch lines, each guarded by
	// the mask: only the server-owned fields a call names are written.
	OutputPatchAssigns []string
	RowToProto         []string // proto field assignments from the row
	NeedsHelpers       helperNeeds

	// Value-object fragments (see vo_gql_view.go). Named apart from the
	// embedded gorm fragments, which speak tx/gorm and must not leak here.
	GQLVOCreates        []string // insert-then-reference before the resource insert
	GQLVOHydrates       []string // follow-up reads inside toProto
	GQLVOStaleVars      []string // stale-id declarations inside Update
	GQLVOUpdates        []string // masked replacements on the patch
	GQLVOStaleDels      []string // stale-row deletions after a successful update
	GQLVODeleteCleanups []string // VO-row removals after the owner row's delete

	// A Batch's writes, queued onto a mutation list (see vo_gql_view.go).
	GQLQueuedCreates   []string // onto gtx, ahead of the row insert
	GQLQueuedUpdates   []string // replacement rows onto utx
	GQLQueuedStaleDels []string // replaced rows onto atx
	GQLQueuedPurges    []string // owned rows onto dtx, read off row
	PKInputField       string   // UpdateInput's key field, for the etag assertion
	// ViaLookups let the list engine filter through the value objects the row
	// points at (filterx.HasuraEngine.Via).
	ViaLookups []viaLookup

	// Deep value-object graph fragments (graph_gql.go); empty without one.
	HasDeep             bool
	InsertResp          string   // the resource's insert response type, for its queued insert
	UpdateResp          string   // its update-by-id response type, for a Batch
	DeleteResp          string   // its delete-by-id response type, for a Batch
	GQLDeepCreates      []string // queued before the resource insert
	GQLDeepAfterCreates []string // queued after it
	GQLDeepStale        []string // captures at the top of Update
	GQLDeepUpdates      []string // replacement ops on the patch / after-tx
	GQLDeepDeletes      []string // subtree removal after the resource delete
	DeepPkgs            []string // client resource packages the fragments use
	DeepPBImports       []string // proto packages of oneof wrappers
}

// helperNeeds tracks which scalar helpers graphql_convert.go must include.
// Wrappers marks google.protobuf wrapper fields (wrapperspb import).
type helperNeeds struct {
	Ts, Date, Struct, Bytes, Duration, StrSlice, Decimal, Enum, Wrappers bool
	Interval                                                             bool // google.type.Interval
	DT                                                                   bool // google.type.DateTime
}

func (h *helperNeeds) or(o helperNeeds) {
	h.Ts = h.Ts || o.Ts
	h.DT = h.DT || o.DT
	h.Date = h.Date || o.Date
	h.Struct = h.Struct || o.Struct
	h.Bytes = h.Bytes || o.Bytes
	h.Duration = h.Duration || o.Duration
	h.StrSlice = h.StrSlice || o.StrSlice
	h.Decimal = h.Decimal || o.Decimal
	h.Enum = h.Enum || o.Enum
	h.Wrappers = h.Wrappers || o.Wrappers
	h.Interval = h.Interval || o.Interval
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
		v.InsertResp = v.ResPkg + ".Insert" + model + "Response"
		v.UpdateResp = v.ResPkg + ".Update" + model + "ByIdResponse"
		v.DeleteResp = v.ResPkg + ".Delete" + model + "ByIdResponse"
		if v.Parented {
			v.ParentPred = v.ResPkg + "." + export(camel(r.ParentFK))
			v.ParentInputField = export(camel(r.ParentFK))
			v.GQLParentAssigns = nil // rebuilt for the client input spelling
			for _, a := range r.AncestorFKs {
				v.GQLParentAssigns = append(v.GQLParentAssigns,
					fmt.Sprintf("ci.%s = parentIDs[%d]", export(camel(a.Column)), a.Index))
			}
		}
		if c := r.Cols.DeleteTime; c != nil {
			v.DeleteTimePred = v.ResPkg + "." + export(camel(c.Name))
			v.DeleteTimeInputField = export(camel(c.Name))
		}
		if c := r.Cols.ExpireTime; c != nil {
			v.ExpireTimeInputField = export(camel(c.Name))
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
		// Server-owned columns are written at create too, as the gorm converter
		// writes every column: a service creating a record sets its state or
		// expiry there, and an API create has already cleared them.
		for _, c := range serverOwned(r) {
			in, patch, _, n, ok := gqlScalarFragments(pb, c, "row."+export(camel(c.Name)))
			if !ok {
				continue
			}
			rn.or(n)
			v.InputAssigns = append(v.InputAssigns, in...)
			v.OutputPatchAssigns = append(v.OutputPatchAssigns,
				fmt.Sprintf("if repox.InMask(paths, %q) {\n\t%s\n\t}", protoField(c), strings.Join(patch, "\n\t")))
		}
		if msg := pb.msgs[r.Table.Source.FullName()]; msg != nil {
			for _, c := range oneofCases(pb, r.Table, msg) {
				v.InputAssigns = append(v.InputAssigns, gqlCaseInput("in", c))
				v.PatchAssigns = append(v.PatchAssigns, gqlCasePatch(c))
			}
		}
		v.NeedsHelpers = rn
		needs.or(rn)
		vp := gqlVOFragments(pb, s, r)
		v.GQLVOCreates = vp.Creates
		v.GQLVOHydrates = vp.Hydrates
		v.GQLVOStaleVars = vp.StaleVars
		v.GQLVOUpdates = vp.Updates
		v.GQLVOStaleDels = vp.StaleDels
		v.GQLVODeleteCleanups = vp.DeleteCleanups
		v.GQLQueuedCreates = vp.QueuedCreates
		v.GQLQueuedUpdates = vp.QueuedUpdates
		v.GQLQueuedStaleDels = vp.QueuedStaleDels
		v.GQLQueuedPurges = vp.QueuedPurges
		for _, g := range vp.VOs {
			v.DeepPkgs = append(v.DeepPkgs, g.VODomPkg+"/"+g.VOResPkg) // the queued fragments name its response types
			v.ViaLookups = append(v.ViaLookups, viaLookup{
				Field: naming.StripIDSuffix(g.vo.Col.Name), Domain: g.VODomain, Resource: g.VOResource, ResPkg: g.VOResPkg,
			})
		}
		for _, e := range r.Graph {
			if e.Kind != edgeOne || e.Natural {
				continue
			}
			nm := gqlNamesOf(e.Child.Table)
			v.ViaLookups = append(v.ViaLookups, viaLookup{
				Field: naming.StripIDSuffix(e.FKCol.Name), Domain: nm.Domain, Resource: nm.Resource, ResPkg: nm.ResPkg,
			})
		}
		v.PKInputField = export(camel(r.Cols.PK.Name))
		gg := graphGQLFragments(pb, r)
		if len(r.Graph) > 0 {
			v.HasDeep = true
			v.InsertResp = v.ResPkg + ".Insert" + model + "Response"
			v.GQLDeepCreates = gg.Creates
			v.GQLDeepAfterCreates = gg.AfterCreates
			v.GQLDeepStale = gg.Stale
			v.GQLDeepUpdates = gg.Updates
			v.GQLDeepDeletes = gg.Deletes
			v.GQLQueuedPurges = append(v.GQLQueuedPurges, gg.QueuedPurges...)
			v.GQLVOHydrates = append(v.GQLVOHydrates, gg.Hydrates...)
			for _, n := range gg.Nodes {
				if n.NaturalPB != "" {
					continue // reached only through the client service: no package reference
				}
				nm := gqlNamesOf(n.Table)
				v.DeepPkgs = append(v.DeepPkgs, nm.DomPkg+"/"+nm.ResPkg)
			}
			for p := range gg.PBImports {
				v.DeepPBImports = append(v.DeepPBImports, p)
			}
		}
		out = append(out, v)
	}
	return out, needs, nil
}

// gqlNodeConvs extends convs with a converter pair for every deep-graph node
// table not already covered, accumulating helper needs.
func gqlNodeConvs(pb *pbIndex, db *schema.Database, s *schema.Schema, resources map[*schema.Table]*resource, convs []gqlVOConv, needs *helperNeeds) []gqlVOConv {
	client := dbGraphQLModule(db)
	have := map[string]bool{}
	for _, c := range convs {
		have[c.ConvName] = true
	}
	var walk func(es []*gEdge)
	walk = func(es []*gEdge) {
		for _, e := range es {
			t := e.Child.Table
			nm := gqlNamesOf(t)
			if !have[nm.ConvName] {
				have[nm.ConvName] = true
				if msg, ok := pb.msgs[t.Source.FullName()]; ok {
					pbImport := string(msg.GoIdent.GoImportPath)
					conv := gqlVOConv{
						ConvName:   nm.ConvName,
						PBType:     pb.names.Of(pbImport) + "." + msg.GoIdent.GoName,
						InputType:  nm.ResPkg + ".CreateInput",
						RowType:    nm.Row,
						ResPkgPath: client + "/" + nm.DomPkg + "/" + nm.ResPkg,
						ResPkgName: nm.ResPkg,
						PBPath:     pbImport,
						PBName:     pb.names.Of(pbImport),
					}
					for _, c := range t.Columns {
						if c.Source == nil || (c.Generated != "") {
							continue
						}
						in, _, row, n, ok := gqlScalarFragments(pb, c, "row."+export(camel(c.Name)))
						if !ok {
							conv.InputAssigns = append(conv.InputAssigns, "// not mapped here: "+c.Name+" (unsupported scalar shape)")
							continue
						}
						if f := pbFieldOf(pb, c); f != nil && f.Enum != nil {
							conv.EnumPaths = append(conv.EnumPaths, string(f.Enum.GoIdent.GoImportPath))
						}
						needs.or(n)
						conv.InputAssigns = append(conv.InputAssigns, in...)
						conv.RowToProto = append(conv.RowToProto, row...)
					}
					for _, c := range oneofCases(pb, t, msg) {
						conv.InputAssigns = append(conv.InputAssigns, gqlCaseInput("in", c))
					}
					convs = append(convs, conv)
				}
			}
			walk(e.Child.Edges)
		}
	}
	for _, r := range sortedResources(s, resources) {
		walk(r.Graph)
	}
	return convs
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
		target := findResourceByModel(db, resources, c.FKModel)
		if c.FKModel != "" && target == nil {
			continue
		}
		if c.FKModel != "" && len(target.Segments) > 1 {
			// A nested resource's name cannot be rebuilt from its last id, so
			// it is stored whole, as written, and crosses unchanged.
			inField := export(camel(c.Name))
			acc := "in.Get" + pbGoField(pb, c) + "()"
			mAcc := "merged.Get" + pbGoField(pb, c) + "()"
			outField := "out." + pbGoField(pb, c)
			inputs = append(inputs, fmt.Sprintf("if v := %s; v != \"\" {\n\t\tci.%s = v\n\t}", acc, inField))
			patches = append(patches, gqlPatch(inField, mAcc, `""`, c.Optional))
			if c.Optional {
				addRow(fmt.Sprintf("%s = repox.Deref(%s)", outField, rowField))
			} else {
				addRow(fmt.Sprintf("%s = %s", outField, rowField))
			}
			continue
		}
		if c.FKModel != "" {
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
			continue
		}
		// Any other stored scalar (a delete_time, a server-computed count) is
		// read back the way a mutable column of its type is, and never
		// written: only the row fragment is kept.
		_, _, row, n, ok := gqlScalarFragments(pb, c, "row."+export(camel(c.Name)))
		if !ok {
			continue
		}
		needs.or(n)
		rows = append(rows, row...)
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
	// A proto3 `optional` scalar carries its presence in the Go field itself (a
	// pointer), so a NULL column reads back as absent and an absent field
	// clears the column, where a plain scalar compares against its zero value.
	f := pbFieldOf(pb, c)
	presence := c.Optional && f != nil && f.Message == nil && f.Oneof != nil && f.Oneof.Desc.IsSynthetic()
	patch := func(expr, zero string) string {
		if !presence {
			return gqlPatch(field, expr, zero, c.Optional)
		}
		return fmt.Sprintf("if merged.%s != nil {\n\t\tpatch.%s = graphql.Value(%s)\n\t} else {\n\t\tpatch.%s = graphql.Null[%s]()\n\t}",
			gof, field, expr, field, patchType(zero))
	}
	// assignRow renders the row->proto assignment. A oneof member has no struct
	// field of its own, so it is boxed in its wrapper and guarded: an absent
	// column must leave the oneof unset rather than select an arm holding nil.
	assignRow := func(expr string) string {
		if presence {
			return fmt.Sprintf("if %s != nil {\n\t\tv := %s\n\t\t%s = &v\n\t}", rowField, expr, outField)
		}
		oneof, wrapper, isOneof := gqlOneofOut(pb, c)
		if !isOneof {
			return fmt.Sprintf("%s = %s", outField, expr)
		}
		// A scalar arm (a string, a number) cannot be compared to nil; its
		// nullable column says whether the arm is set by being non-NULL.
		scalar := false
		switch c.Type {
		case schema.TypeString, schema.TypeText, schema.TypeEnum, schema.TypeInt32, schema.TypeUint32,
			schema.TypeInt64, schema.TypeUint64, schema.TypeFloat, schema.TypeDouble, schema.TypeBool, schema.TypeDecimal:
			scalar = true
		}
		if scalar && c.Optional {
			return fmt.Sprintf("if %s != nil {\n\t\tout.%s = &%s{%s: %s}\n\t}", rowField, oneof, wrapper, gof, expr)
		}
		return fmt.Sprintf("if v := %s; v != nil {\n\t\tout.%s = &%s{%s: v}\n\t}", expr, oneof, wrapper, gof)
	}
	rowVal := rowField
	if c.Optional {
		rowVal = "repox.Deref(" + rowField + ")"
	}

	// google.protobuf wrapper fields: nil-able scalars on both sides.
	if f != nil && f.Message != nil && !c.List {
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
		patches = append(patches, patch(patchExpr, zero))
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
		patches = append(patches, patch(toStr(mAcc), fmt.Sprintf("%q", "UNSPECIFIED")))
		// The stored label is the value name without the enum's type prefix.
		// An enum whose values carry no such prefix (google.type.DayOfWeek's
		// MONDAY, not DAY_OF_WEEK_MONDAY) is looked up by the bare label.
		readPrefix := prefix
		if f := pbFieldOf(pb, c); f != nil && f.Enum != nil && !enumValuesPrefixed(f.Enum, prefix) {
			readPrefix = ""
		}
		if readPrefix == "" {
			rows = append(rows, assignRow(fmt.Sprintf("%s(%s_value[%s])", pbT, pbT, rowVal)))
		} else {
			rows = append(rows, assignRow(fmt.Sprintf("%s(%s_value[%q+%s])", pbT, pbT, readPrefix, rowVal)))
		}
	case schema.TypeInt32, schema.TypeUint32:
		set(fmt.Sprintf("int32(%s)", acc), fmt.Sprintf("int32(%s)", mAcc), fmt.Sprintf("%s(%s)", pbCast(pb, c), rowVal), "0")
	case schema.TypeInt64, schema.TypeUint64:
		inputs = append(inputs, fmt.Sprintf("ci.%s = graphql.Int64(%s)", field, acc))
		patches = append(patches, patch(fmt.Sprintf("graphql.Int64(%s)", mAcc), "graphql.Int64(0)"))
		rows = append(rows, assignRow(fmt.Sprintf("%s(%s)", pbCast(pb, c), rowVal)))
	case schema.TypeFloat, schema.TypeDouble:
		set(acc, mAcc, rowVal, "0.0")
	case schema.TypeDecimal:
		needs.Decimal = true
		inputs = append(inputs, fmt.Sprintf("ci.%s = bigdec(%s)", field, acc))
		patches = append(patches, patch(fmt.Sprintf("bigdec(%s)", mAcc), `graphql.Bigdecimal("")`))
		rows = append(rows, assignRow(fmt.Sprintf("fromBigdec(%s)", rowVal)))
	case schema.TypeBool:
		set(acc, mAcc, rowVal, "false")
	case schema.TypeTimestamp:
		// protokit maps more than one well-known type onto this schema type
		// (TypeTimestamp covers google.type.DateTime too), and they are different
		// Go types. Emitting the helper for the wrong one does not compile, so an
		// unrecognised message is skipped as an unsupported shape — the same guard
		// the gorm converter applies.
		if f := pbFieldOf(pb, c); f != nil && f.Message != nil && string(f.Message.Desc.FullName()) == "google.type.DateTime" {
			// A civil time with a zone or offset, stored as the instant it
			// denotes; same mapping as the gorm converter.
			needs.DT = true
			// The zone it was written in rides in the companion column the
			// IR adds beside every DateTime (package dtzone).
			zone := export(camel(dtzone.Name(c)))
			inputs = append(inputs, fmt.Sprintf("if v := dtToStr(%s); v != \"\" {\n\t\tci.%s = v\n\t}", acc, field),
				fmt.Sprintf("if z := dtZoneOf(%s); z != nil {\n\t\tci.%s = *z\n\t}", acc, zone))
			patches = append(patches, patch(fmt.Sprintf("dtToStr(%s)", mAcc), `""`),
				fmt.Sprintf("if z := dtZoneOf(%s); z != nil {\n\t\tpatch.%s = graphql.Value(*z)\n\t} else {\n\t\tpatch.%s = graphql.Null[string]()\n\t}", mAcc, zone, zone))
			rows = append(rows, assignRow(fmt.Sprintf("strToDt(%s, row.%s)", rowVal, zone)))
			return inputs, patches, rows, needs, true
		}
		if f := pbFieldOf(pb, c); f != nil && f.Message != nil && string(f.Message.Desc.FullName()) != "google.protobuf.Timestamp" {
			return nil, nil, nil, needs, false
		}
		needs.Ts = true
		inputs = append(inputs, fmt.Sprintf("ci.%s = tsToStr(%s)", field, acc))
		patches = append(patches, patch(fmt.Sprintf("tsToStr(%s)", mAcc), `""`))
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
		patches = append(patches, patch(fmt.Sprintf("dateToStr(%s)", mAcc), `""`))
		rows = append(rows, assignRow(fmt.Sprintf("strToDate(%s)", rowVal)))
	case schema.TypeInterval:
		// A google.type.Interval crosses as the same "start/end" text the gorm
		// converter stores, so both adapters read each other's rows.
		if f := pbFieldOf(pb, c); f == nil || f.Message == nil || string(f.Message.Desc.FullName()) != "google.type.Interval" {
			return nil, nil, nil, needs, false
		}
		needs.Interval = true
		inputs = append(inputs, fmt.Sprintf("if v := intervalToStr(%s); v != \"\" {\n\t\tci.%s = v\n\t}", acc, field))
		patches = append(patches, patch(fmt.Sprintf("intervalToStr(%s)", mAcc), `""`))
		rows = append(rows, assignRow(fmt.Sprintf("strToInterval(%s)", rowVal)))
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
		patches = append(patches, patch(fmt.Sprintf("durToStr(%s)", mAcc), `""`))
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

// enumValuesPrefixed reports whether e's non-zero values are spelled with
// prefix (MEMBER_ROLE_ADMIN for MemberRole) rather than bare (MONDAY).
func enumValuesPrefixed(e *protogen.Enum, prefix string) bool {
	for _, v := range e.Values {
		if v.Desc.Number() == 0 {
			continue
		}
		return strings.HasPrefix(string(v.Desc.Name()), prefix)
	}
	return true
}

// viaLookup is one value object the GraphQL list engine can filter through:
// the reference field the filter spec names it by and the client handler of
// its table.
type viaLookup struct {
	Field    string // e.g. "start"
	Domain   string
	Resource string
	ResPkg   string
}
