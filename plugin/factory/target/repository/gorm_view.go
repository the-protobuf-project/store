// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// gorm_view.go prepares the per-schema names.go / mask.go / gorm.go views. The
// intricate per-column pieces (reference name↔id mapping, mutable write-back)
// are rendered here as code fragments; the templates splice them.

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
)

// gormResourceView is the adapter-facing view of one resource, embedding the
// neutral resourceView the interface template also uses.
type gormResourceView struct {
	resourceView
	Store string // generated store constructor, e.g. "NewMemberStore"

	HasEtag   bool
	EtagField string // gorm model field, e.g. "Etag"; pointer-typed when EtagPtr
	EtagPtr   bool

	PKField string // gorm model field of the surrogate key, e.g. "ID"

	// Name codec inputs.
	Vars               []string // pattern variable names, e.g. organisation, member
	VarList            string   // comma-joined Vars for parameter lists
	FormatExpr         string   // rendered name-building expression
	CollectionsExpr    string   // quoted, comma-joined own collections for SplitName
	ParentCollExpr     string   // likewise for the parent name (parented only)
	ParentVarList      string   // comma-joined parent vars (parented only)
	ParentFKField      string   // gorm model field of the parent FK, e.g. "OrganisationID"
	ParentFKColumn     string   // parent FK column name, e.g. "organisation_id"
	ParentAssigns      []string // every ancestor FK assignment from the split parent name
	GQLParentAssigns   []string // the same wiring on the client CreateInput
	FormatCallParented string   // Format call from parentIDs + id (parented only)
	FormatCallRoot     string   // Format call from the bare id (root only)

	// Rendered fragments.
	RefsCreate     []string // set model ref columns from the incoming proto
	RefsToProto    []string // decorate the proto with formatted reference names
	MutableAssigns []string // write-back of mutable columns onto the loaded row
	MaskFields     []maskFieldView

	// Value-object fragments (see vo_view.go).
	HasVOs           bool
	Preloads         string   // .Preload chain for get/list/update reads
	VOCreates        []string // create-in-transaction fragments
	VOStaleVars      []string // stale-id declarations inside the update transaction
	VOUpdates        []string // masked replace fragments
	VOStaleDels      []string // stale-row deletions after the row update
	VODeleteCleanups []string // VO-row removals inside the delete transaction
	VOMaskLines      []string // VO merge lines spliced into apply<X>Mask
	CrossVOPkgs      []string // cross-schema gorm packages the fragments reference
}

// maskFieldView is one mutable proto field in the generated mask-apply.
type maskFieldView struct {
	Path    string // proto field name, e.g. "display_name"
	GoField string // proto Go field, e.g. "DisplayName"
	Message bool   // message-typed: matched by GroupTouched (prefix) semantics
}

// gormResourceViews builds the adapter views for schema s in table order.
func gormResourceViews(pb *pbIndex, db *schema.Database, s *schema.Schema, resources map[*schema.Table]*resource, ifaceViews []resourceView) ([]gormResourceView, error) {
	rs := sortedResources(s, resources)
	out := make([]gormResourceView, 0, len(rs))
	gormPkg := gormModelsQual(pb, s, naming.GoPackage(s.Name))
	for i, r := range rs {
		v := gormResourceView{
			resourceView: ifaceViews[i],
			Store:        "New" + r.Table.LocalName + "Store",
		}
		if r.Cols.PK != nil {
			v.PKField = gormField(r.Cols.PK)
		} else {
			return nil, fmt.Errorf("repository: %s has no generated surrogate key (id strategy required)", r.Table.ProtoMessage)
		}
		if r.Cols.Etag != nil {
			v.HasEtag, v.EtagField, v.EtagPtr = true, gormField(r.Cols.Etag), r.Cols.Etag.Optional
		}
		var formatParts, collections []string
		for _, seg := range r.Segments {
			varName := naming.Camel(seg.Var)
			v.Vars = append(v.Vars, varName)
			formatParts = append(formatParts, fmt.Sprintf("%q+\"/\"+%s", seg.Collection, varName))
			collections = append(collections, fmt.Sprintf("%q", seg.Collection))
		}
		v.VarList = strings.Join(v.Vars, ", ")
		v.FormatExpr = strings.Join(formatParts, "+\"/\"+")
		v.CollectionsExpr = strings.Join(collections, ", ")
		if v.Parented {
			v.ParentCollExpr = strings.Join(collections[:len(collections)-1], ", ")
			v.ParentVarList = strings.Join(v.Vars[:len(v.Vars)-1], ", ")
			v.ParentFKField = gormFieldName(r.ParentFK, false)
			v.ParentFKColumn = r.ParentFK
			for _, a := range r.AncestorFKs {
				v.ParentAssigns = append(v.ParentAssigns,
					fmt.Sprintf("m.%s = parentIDs[%d]", gormFieldName(a.Column, false), a.Index))
			}
			args := make([]string, 0, len(r.Segments))
			for i := range r.Segments[:len(r.Segments)-1] {
				args = append(args, fmt.Sprintf("parentIDs[%d]", i))
			}
			v.FormatCallParented = fmt.Sprintf("Format%sName(%s)", r.Table.LocalName, strings.Join(append(args, "id"), ", "))
		} else {
			v.FormatCallRoot = fmt.Sprintf("Format%sName(id)", r.Table.LocalName)
		}
		for _, c := range r.Cols.Refs {
			ref, ok := refFragments(pb, db, resources, r, c)
			if !ok {
				continue // nested-resource reference: Tier-2 territory
			}
			v.RefsCreate = append(v.RefsCreate, ref.create)
			v.RefsToProto = append(v.RefsToProto, ref.toProto)
		}
		v.MutableAssigns, v.MaskFields = mutableFragments(pb, db, resources, r)
		vg := voGormFragments(gormPkg, r)
		v.HasVOs = len(r.VOs) > 0
		v.Preloads = vg.Preloads
		v.VOCreates = vg.Creates
		v.VOStaleVars = vg.StaleVars
		v.VOUpdates = vg.Updates
		v.VOStaleDels = vg.StaleDels
		v.VODeleteCleanups = vg.DeleteCleanups
		v.VOMaskLines = vg.MaskLines
		v.CrossVOPkgs = vg.CrossPkgs
		out = append(out, v)
	}
	return out, nil
}

// refFragment holds the rendered pieces for one root-resource reference column.
type refFragment struct{ create, toProto string }

// refFragments renders the name↔bare-id mapping for a reference column. Only
// references to ROOT resources (single-segment patterns) are generated — a
// nested resource's name cannot be rebuilt from the stored leaf id alone, so
// those stay with Tier-2 overrides, mirroring the gorm converters' contract.
func refFragments(pb *pbIndex, db *schema.Database, resources map[*schema.Table]*resource, r *resource, c *schema.Column) (refFragment, bool) {
	target := findResourceByModel(db, resources, c.FKModel)
	if target == nil || len(target.Segments) != 1 {
		return refFragment{}, false
	}
	collection := target.Segments[0].Collection
	field := gormField(c)                     // e.g. "UserID"
	acc := "in.Get" + pbGoField(pb, c) + "()" // e.g. in.GetUser()
	outField := "out." + pbGoField(pb, c)
	var frag refFragment
	if c.Optional {
		frag.create = fmt.Sprintf("if v := %s; v != \"\" {\n\t\tm.%s = repox.Ptr(repox.LastSegment(v))\n\t}", acc, field)
		frag.toProto = fmt.Sprintf("if m.%s != nil && *m.%s != \"\" {\n\t\t%s = %q + *m.%s\n\t}", field, field, outField, collection+"/", field)
	} else {
		frag.create = fmt.Sprintf("if v := %s; v != \"\" {\n\t\tm.%s = repox.LastSegment(v)\n\t}", acc, field)
		frag.toProto = fmt.Sprintf("if m.%s != \"\" {\n\t\t%s = %q + m.%s\n\t}", field, outField, collection+"/", field)
	}
	return frag, true
}

// mutableFragments renders the update write-back assignments and the mask
// field list for r's mutable columns. Plain columns copy from the re-converted
// model (same Go types on both sides); reference columns re-derive the bare id
// from the merged proto (the converters skip them).
func mutableFragments(pb *pbIndex, db *schema.Database, resources map[*schema.Table]*resource, r *resource) ([]string, []maskFieldView) {
	var assigns []string
	var masks []maskFieldView
	for _, c := range r.Cols.Mutable {
		pf := protoField(c)
		if c.FKModel != "" {
			if target := findResourceByModel(db, resources, c.FKModel); target == nil || len(target.Segments) != 1 {
				continue // nested-resource reference: Tier-2
			}
			field := gormField(c)
			acc := "merged.Get" + pbGoField(pb, c) + "()"
			if c.Optional {
				assigns = append(assigns, fmt.Sprintf("if v := %s; v != \"\" {\n\t\t\texisting.%s = repox.Ptr(repox.LastSegment(v))\n\t\t} else {\n\t\t\texisting.%s = nil\n\t\t}", acc, field, field))
			} else {
				assigns = append(assigns, fmt.Sprintf("existing.%s = repox.LastSegment(%s)", field, acc))
			}
		} else {
			field := gormField(c)
			assigns = append(assigns, fmt.Sprintf("existing.%s = next.%s", field, field))
		}
		masks = append(masks, maskFieldView{
			Path:    pf,
			GoField: pbGoField(pb, c),
			Message: isMessageField(c),
		})
	}
	return assigns, masks
}

// findResourceByModel resolves a reference column's target resource anywhere
// in the database by its singular model name.
func findResourceByModel(db *schema.Database, resources map[*schema.Table]*resource, model string) *resource {
	for _, s := range db.Schemas {
		for _, t := range s.Tables {
			if t.LocalName == model || t.ModelName == model {
				if r, ok := resources[t]; ok {
					return r
				}
			}
		}
	}
	return nil
}

// protoField is the proto field name behind a column (its Source name), or the
// column name for synthesized columns.
func protoField(c *schema.Column) string {
	if c.Source != nil {
		return string(c.Source.Name())
	}
	return c.Name
}

// isMessageField reports whether the column's proto field is message-typed
// (dates, structs, nested messages) — mask-matched with prefix semantics.
func isMessageField(c *schema.Column) bool {
	return c.Source != nil && c.Source.Message() != nil
}

// pbGoField is the generated PROTO Go field for a column — resolved from
// protogen's authoritative GoName (protoc-gen-go does not Go-normalize
// initialisms: avatar_url → AvatarUrl, never AvatarURL), with a plain
// per-part capitalization fallback for fields protogen didn't load.
func pbGoField(pb *pbIndex, c *schema.Column) string {
	if c.Source != nil {
		if m, ok := pb.msgs[c.Source.ContainingMessage().FullName()]; ok {
			for _, f := range m.Fields {
				if f.Desc.FullName() == c.Source.FullName() {
					return f.GoName
				}
			}
		}
	}
	parts := strings.Split(protoField(c), "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}

// gormField is the generated gorm model's Go field for a column, mirroring the
// gorm target's naming (FK columns carry an ID suffix even when the column
// name doesn't).
func gormField(c *schema.Column) string { return gormFieldName(c.Name, c.FKModel != "") }

func gormFieldName(col string, isFK bool) string {
	return naming.PascalGo(naming.FKFieldBase(col, isFK))
}
