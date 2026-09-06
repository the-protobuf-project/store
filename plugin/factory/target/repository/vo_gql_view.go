// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// vo_gql_view.go renders the value-object fragments for the GraphQL adapters:
// insert-then-reference on create, per-row hydration on reads (the FK id
// resolves through a follow-up Get, like the hand-written repositories did),
// masked wholesale replacement on update, and the shared VO converters
// (proto ↔ CreateInput/row) emitted once per schema into protobuf.go.

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
)

// gqlVO is one value object as the GraphQL adapter sees it.
type gqlVO struct {
	vo voField

	VODomain   string // client domain field, e.g. "Shared"
	VOResource string // client resource field, e.g. "TimeWindows"
	VOResPkg   string // client resource package, e.g. "timewindowsql"
	VODomPkg   string // client domain package, e.g. "sharedql"
	VORowQual  string // qualified row type, e.g. "timewindowsql.SharedTimeWindows"
	ConvName   string // converter base, e.g. "sharedTimeWindow"
	PBType     string // qualified VO proto type, e.g. "sharedpbv1.TimeWindow"
	PBImport   string // import path of the VO proto package
	Wrapper    string // qualified oneof wrapper type ("" plain field)

	FKInput string // resource CreateInput/patch/row field, e.g. "WindowId"
}

// gqlVOPlan is the per-resource bundle of rendered GraphQL VO fragments.
type gqlVOPlan struct {
	Creates        []string // in → ci fragments before the resource insert
	Hydrates       []string // row → out fragments inside toProto
	StaleVars      []string // stale-id declarations inside Update
	Updates        []string // merged → patch fragments before the mutation
	StaleDels      []string // stale-row deletions after a successful update
	DeleteCleanups []string // VO-row removals after the owner row's delete
	VOs            []gqlVO
}

// gqlVOFor resolves the client-side naming of one planned VO for a resource
// in schema s. Row types live in the VO's own resource package (imported for
// its CreateInput anyway), so same-schema and foreign VOs qualify alike.
func gqlVOFor(pb *pbIndex, s *schema.Schema, v voField) (gqlVO, bool) {
	msg, ok := pb.msgs[v.Target.Source.FullName()]
	if !ok {
		return gqlVO{}, false
	}
	voSchema := v.Target.PgSchema
	domain := naming.PascalGo(voSchema)
	model := domain + naming.PascalGo(v.Target.Name)
	rest := strings.TrimPrefix(model, domain)
	rowQual := clientPkgIdent(rest) + "." + model
	g := gqlVO{
		vo:         v,
		VODomain:   domain,
		VOResource: rest,
		VOResPkg:   clientPkgIdent(rest),
		VODomPkg:   identLower(voSchema) + "ql",
		VORowQual:  rowQual,
		ConvName:   naming.CamelFirst(domain + v.Target.LocalName),
		PBType:     pb.names.Of(string(msg.GoIdent.GoImportPath)) + "." + msg.GoIdent.GoName,
		PBImport:   string(msg.GoIdent.GoImportPath),
		FKInput:    export(camel(v.Col.Name)),
	}
	return g, true
}

// gqlVOFragments renders the GraphQL adapter fragments for r's VOs. resPB is
// the resource's qualified proto type (for oneof wrapper names) and caseInput
// naming rides the same export(camel(...)) rule as every other column.
func gqlVOFragments(pb *pbIndex, s *schema.Schema, r *resource) gqlVOPlan {
	var plan gqlVOPlan
	if len(r.VOs) == 0 {
		return plan
	}
	byField := map[string]gqlVO{}
	for _, v := range r.VOs {
		g, ok := gqlVOFor(pb, s, v)
		if !ok {
			continue
		}
		if v.Case != nil {
			g.Wrapper = oneofWrapperType(pb, r, v)
			if g.Wrapper == "" {
				continue // cannot name the wrapper: leave the whole VO out
			}
		}
		plan.VOs = append(plan.VOs, g)
		byField[v.FieldName] = g
	}

	for _, g := range plan.VOs {
		plan.Creates = append(plan.Creates, gqlVOCreate(g, "in", "ci"))
		plan.Hydrates = append(plan.Hydrates, gqlVOHydrate(g))
		plan.StaleVars = append(plan.StaleVars, fmt.Sprintf("var stale%s string", g.vo.PBGoName))
		plan.DeleteCleanups = append(plan.DeleteCleanups, gqlVODeleteCleanup(g))
		plan.StaleDels = append(plan.StaleDels, fmt.Sprintf(
			"if stale%s != \"\" {\n\t\tif _, err := r.Svc.Mutation.%s.%s.Delete(ctx, stale%s); err != nil {\n\t\t\treturn nil, mapGraphQLErr(err)\n\t\t}\n\t}",
			g.vo.PBGoName, g.VODomain, g.VOResource, g.vo.PBGoName))
	}
	for _, grp := range voGroups(r.VOs) {
		var gs []gqlVO
		for _, v := range grp {
			if g, ok := byField[v.FieldName]; ok {
				gs = append(gs, g)
			}
		}
		if len(gs) > 0 {
			plan.Updates = append(plan.Updates, gqlVOUpdate(gs))
		}
	}
	return plan
}

// oneofWrapperType resolves the qualified Go wrapper type of a oneof member
// field on the resource message (e.g. "schedulepbv1.AvailabilityException_Window").
func oneofWrapperType(pb *pbIndex, r *resource, v voField) string {
	msg, ok := pb.msgs[r.Table.Source.FullName()]
	if !ok {
		return ""
	}
	for _, f := range msg.Fields {
		if string(f.Desc.Name()) == v.FieldName {
			return pb.names.Of(string(f.GoIdent.GoImportPath)) + "." + f.GoIdent.GoName
		}
	}
	return ""
}

// caseInputField is the resource-side input/row spelling of the discriminator.
func caseInputField(v voField) string { return export(camel(v.Case.Column.Name)) }

// caseValue is the stored discriminator label for a member (the proto oneof
// member name in SCREAMING_SNAKE, matching the synthesized check constraint).
func caseValue(v voField) string { return naming.ScreamingSnake(v.FieldName) }

// gqlVOCreate renders "insert the VO row, then reference it from the resource
// input" for one member; the discriminator rides the same input.
func gqlVOCreate(g gqlVO, src, dst string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "if v := %s.Get%s(); v != nil {\n", src, g.vo.PBGoName)
	fmt.Fprintf(&b, "\t\tvi := %sToCreateInput(v)\n", g.ConvName)
	b.WriteString("\t\tvi.Id = repox.NewULID()\n")
	fmt.Fprintf(&b, "\t\tif _, err := r.Svc.Mutation.%s.%s.Create(ctx, vi); err != nil {\n\t\t\treturn nil, mapGraphQLErr(err)\n\t\t}\n", g.VODomain, g.VOResource)
	fmt.Fprintf(&b, "\t\t%s.%s = vi.Id\n", dst, g.FKInput)
	if g.vo.Case != nil {
		fmt.Fprintf(&b, "\t\t%s.%s = %q\n", dst, caseInputField(g.vo), caseValue(g.vo))
	}
	b.WriteString("\t}")
	return b.String()
}

// gqlVOHydrate renders the follow-up read that turns a stored FK id back into
// the proto message field.
func gqlVOHydrate(g gqlVO) string {
	deref := "row." + g.FKInput
	if g.vo.Col.Optional {
		deref = "repox.Deref(row." + g.FKInput + ")"
	}
	var assign string
	if g.Wrapper != "" {
		assign = fmt.Sprintf("out.%s = &%s{%s: %sFromRow(w)}", g.vo.Case.OneofGoName, g.Wrapper, g.vo.PBGoName, g.ConvName)
	} else {
		assign = fmt.Sprintf("out.%s = %sFromRow(w)", g.vo.PBGoName, g.ConvName)
	}
	return fmt.Sprintf(
		"if id := %s; id != \"\" {\n\t\tw, err := r.Svc.Query.%s.%s.Get(ctx, id)\n\t\tif err != nil {\n\t\t\treturn nil, mapGraphQLErr(err)\n\t\t}\n\t\tif w != nil {\n\t\t\t%s\n\t\t}\n\t}",
		deref, g.VODomain, g.VOResource, assign)
}

// gqlVODeleteCleanup renders the removal of a VO row after its owner's
// delete, reading the stored reference off the mutation's returning set.
func gqlVODeleteCleanup(g gqlVO) string {
	idExpr := "resp.Returning[i]." + g.FKInput
	if g.vo.Col.Optional {
		idExpr = "repox.Deref(resp.Returning[i]." + g.FKInput + ")"
	}
	return fmt.Sprintf(
		"for i := range resp.Returning {\n\t\tif id := %s; id != \"\" {\n\t\t\tif _, err := r.Svc.Mutation.%s.%s.Delete(ctx, id); err != nil {\n\t\t\t\treturn mapGraphQLErr(err)\n\t\t\t}\n\t\t}\n\t}",
		idExpr, g.VODomain, g.VOResource)
}

// gqlVOUpdate renders the masked replacement of one VO group on the patch:
// remember the stored ids, null the references (and discriminator), and insert
// whichever member the merged record carries. Stale rows are deleted after the
// resource mutation succeeds.
func gqlVOUpdate(grp []gqlVO) string {
	var conds []string
	for _, g := range grp {
		conds = append(conds, fmt.Sprintf("repox.GroupTouched(paths, %q)", g.vo.FieldName))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "if %s {\n", strings.Join(conds, " || "))
	for _, g := range grp {
		if g.vo.Col.Optional {
			fmt.Fprintf(&b, "\t\tstale%s = repox.Deref(row.%s)\n", g.vo.PBGoName, g.FKInput)
			fmt.Fprintf(&b, "\t\tpatch.%s = graphql.Null[string]()\n", g.FKInput)
		} else {
			fmt.Fprintf(&b, "\t\tstale%s = row.%s\n", g.vo.PBGoName, g.FKInput)
		}
	}
	if c := grp[0].vo.Case; c != nil {
		if c.Column.Optional {
			fmt.Fprintf(&b, "\t\tpatch.%s = graphql.Null[string]()\n", caseInputField(grp[0].vo))
		}
	}
	for _, g := range grp {
		fmt.Fprintf(&b, "\t\tif v := merged.Get%s(); v != nil {\n", g.vo.PBGoName)
		fmt.Fprintf(&b, "\t\t\tvi := %sToCreateInput(v)\n", g.ConvName)
		b.WriteString("\t\t\tvi.Id = repox.NewULID()\n")
		fmt.Fprintf(&b, "\t\t\tif _, err := r.Svc.Mutation.%s.%s.Create(ctx, vi); err != nil {\n\t\t\t\treturn nil, mapGraphQLErr(err)\n\t\t\t}\n", g.VODomain, g.VOResource)
		fmt.Fprintf(&b, "\t\t\tpatch.%s = graphql.Value(vi.Id)\n", g.FKInput)
		if g.vo.Case != nil {
			fmt.Fprintf(&b, "\t\t\tpatch.%s = graphql.Value(%q)\n", caseInputField(g.vo), caseValue(g.vo))
		}
		b.WriteString("\t\t}\n")
	}
	b.WriteString("\t}")
	return b.String()
}

// gqlVOConv is one VO converter pair (proto→CreateInput, row→proto) emitted
// into the schema's protobuf.go; deduplicated per VO table. The import fields
// let the file view pull in exactly the client and pb packages the pair uses.
type gqlVOConv struct {
	ConvName     string
	PBType       string
	InputType    string // e.g. "timewindowsql.CreateInput"
	RowType      string // e.g. "timewindowsql.SharedTimeWindows"
	InputAssigns []string
	RowToProto   []string

	ResPkgPath string // client resource package import
	ResPkgName string
	PBPath     string // VO proto package import
	PBName     string
}

// gqlVOConvs builds the deduplicated converter list for every VO the schema's
// resources use, accumulating the scalar helper needs.
func gqlVOConvs(pb *pbIndex, db *schema.Database, s *schema.Schema, resources map[*schema.Table]*resource, needs *helperNeeds) []gqlVOConv {
	client := dbGraphQLModule(db)
	seen := map[*schema.Table]bool{}
	var out []gqlVOConv
	for _, r := range sortedResources(s, resources) {
		for _, v := range r.VOs {
			if seen[v.Target] {
				continue
			}
			seen[v.Target] = true
			g, ok := gqlVOFor(pb, s, v)
			if !ok {
				continue
			}
			conv := gqlVOConv{
				ConvName:   g.ConvName,
				PBType:     g.PBType,
				InputType:  g.VOResPkg + ".CreateInput",
				RowType:    g.VORowQual,
				ResPkgPath: client + "/" + g.VODomPkg + "/" + g.VOResPkg,
				ResPkgName: g.VOResPkg,
				PBPath:     g.PBImport,
				PBName:     pb.names.Of(g.PBImport),
			}
			for _, c := range v.Target.Columns {
				if c.Source == nil || c.Generated != "" {
					continue
				}
				in, _, row, n, ok := gqlScalarFragments(pb, c, "row."+export(camel(c.Name)))
				if !ok {
					conv.InputAssigns = append(conv.InputAssigns, "// not mapped here: "+c.Name+" (unsupported scalar shape)")
					continue
				}
				needs.or(n)
				conv.InputAssigns = append(conv.InputAssigns, in...)
				conv.RowToProto = append(conv.RowToProto, row...)
			}
			out = append(out, conv)
		}
	}
	return out
}
