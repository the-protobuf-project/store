// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// vo_view.go renders the value-object fragments the gorm adapter splices into
// its CRUD bodies: create-in-transaction, Preload chains for reads, masked
// wholesale replacement on update (fresh row + stale-row cleanup), and the
// oneof group merges for mask.go. All decisions are made here; the template
// only splices.

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/naming"
)

// voGorm is the per-resource bundle of rendered gorm VO fragments.
type voGorm struct {
	Preloads       string   // .Preload("X") chain appended to read queries
	Creates        []string // in → m fragments inside the create transaction
	StaleVars      []string // stale-id declarations at the top of the update transaction
	Updates        []string // merged → existing fragments inside the update transaction
	StaleDels      []string // stale-row deletions after the row update
	DeleteCleanups []string // VO-row removals after the owner row's delete
	MaskLines      []string // oneof group merges spliced into apply<X>Mask
	CrossPkgs      []string // gorm packages of cross-schema value objects (imports)
}

// voGormFragments renders every VO fragment for r. pkg is the identifier the
// adapter file binds to the resource schema's gorm models package — its last
// path segment normally, an alias when a proto package in the same file already
// claims that name (see gormModelsQual); cross-schema value objects qualify
// through their own package.
func voGormFragments(pkg string, r *resource) voGorm {
	var out voGorm
	if len(r.VOs) == 0 {
		return out
	}
	var preloads []string
	cross := map[string]bool{}
	for _, v := range r.VOs {
		preloads = append(preloads, fmt.Sprintf(".Preload(%q)", v.AssocField))
		if v.CrossPkg != "" && !cross[v.CrossPkg] {
			cross[v.CrossPkg] = true
			out.CrossPkgs = append(out.CrossPkgs, v.CrossPkg)
		}
		out.Creates = append(out.Creates, voWriteFragment(pkg, v, "in", "m"))
		out.StaleVars = append(out.StaleVars, fmt.Sprintf("var stale%s string", v.PBGoName))
		out.StaleDels = append(out.StaleDels, voStaleDelete(pkg, v))
		out.DeleteCleanups = append(out.DeleteCleanups, voDeleteCleanup(pkg, v))
	}
	out.Preloads = strings.Join(preloads, "")

	// Updates and mask lines work on oneof groups: touching any member
	// replaces the whole group (a oneof holds at most one member).
	for _, grp := range voGroups(r.VOs) {
		out.Updates = append(out.Updates, voUpdateFragment(pkg, grp))
		if grp[0].Case != nil {
			var conds []string
			for _, v := range grp {
				conds = append(conds, fmt.Sprintf("repox.GroupTouched(paths, %q)", v.FieldName))
			}
			out.MaskLines = append(out.MaskLines, fmt.Sprintf(
				"if %s {\n\t\tmerged.%s = in.%s\n\t}",
				strings.Join(conds, " || "), grp[0].Case.OneofGoName, grp[0].Case.OneofGoName))
		} else {
			out.MaskLines = append(out.MaskLines, fmt.Sprintf(
				"if repox.GroupTouched(paths, %q) {\n\t\tmerged.%s = in.Get%s()\n\t}",
				grp[0].FieldName, grp[0].PBGoName, grp[0].PBGoName))
		}
	}
	return out
}

// voGroups buckets VOs so each real oneof forms one group (in declaration
// order) and every plain field stands alone.
func voGroups(vos []voField) [][]voField {
	var groups [][]voField
	byOneof := map[string]int{}
	for _, v := range vos {
		if v.Case == nil {
			groups = append(groups, []voField{v})
			continue
		}
		if i, ok := byOneof[v.Case.OneofGoName]; ok {
			groups[i] = append(groups[i], v)
			continue
		}
		byOneof[v.Case.OneofGoName] = len(groups)
		groups = append(groups, []voField{v})
	}
	return groups
}

// voQual is the qualified gorm identifier prefix of the VO's model/converters.
func voQual(pkg string, v voField) string {
	if v.CrossPkg != "" {
		return v.CrossPkg
	}
	return pkg
}

// voPKField is the gorm field of the VO table's generated surrogate key.
func voPKField(v voField) string {
	for _, c := range v.Target.Columns {
		if c.Generated != "" {
			return naming.PascalGo(naming.FKFieldBase(c.Name, false))
		}
	}
	return "ID"
}

// voWriteFragment renders "when the proto field is set, insert a fresh VO row
// and point the resource row at it (setting the oneof discriminator)". src is
// the proto variable, dst the row variable; tx and ctx are in scope at every
// splice point.
func voWriteFragment(pkg string, v voField, src, dst string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "if v := %s.Get%s(); v != nil {\n", src, v.PBGoName)
	fmt.Fprintf(&b, "\t\tvo := %s.%sFromProto(v)\n", voQual(pkg, v), v.Target.LocalName)
	fmt.Fprintf(&b, "\t\tvo.%s = repox.NewULID()\n", voPKField(v))
	b.WriteString("\t\tif err := tx.WithContext(ctx).Create(vo).Error; err != nil {\n\t\t\treturn err\n\t\t}\n")
	if v.Col.Optional {
		fmt.Fprintf(&b, "\t\t%s.%s = repox.Ptr(vo.%s)\n", dst, v.FKField, voPKField(v))
	} else {
		fmt.Fprintf(&b, "\t\t%s.%s = vo.%s\n", dst, v.FKField, voPKField(v))
	}
	if v.Case != nil {
		fmt.Fprintf(&b, "\t\tc := %s.%s\n", pkg, v.Case.Const)
		if v.Case.Column.Optional {
			fmt.Fprintf(&b, "\t\t%s.%s = &c\n", dst, v.Case.Field)
		} else {
			fmt.Fprintf(&b, "\t\t%s.%s = c\n", dst, v.Case.Field)
		}
	}
	b.WriteString("\t}")
	return b.String()
}

// voUpdateFragment renders the masked wholesale replacement of one VO group:
// remember the current rows, detach them, and re-create whichever member the
// merged record carries. Stale rows are deleted after the resource row update
// (see voStaleDelete), so the foreign keys never dangle.
func voUpdateFragment(pkg string, grp []voField) string {
	var conds []string
	for _, v := range grp {
		conds = append(conds, fmt.Sprintf("repox.GroupTouched(paths, %q)", v.FieldName))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "if %s {\n", strings.Join(conds, " || "))
	for _, v := range grp {
		if v.Col.Optional {
			fmt.Fprintf(&b, "\t\t\tif existing.%s != nil {\n\t\t\t\tstale%s = *existing.%s\n\t\t\t}\n", v.FKField, v.PBGoName, v.FKField)
			fmt.Fprintf(&b, "\t\t\texisting.%s = nil\n", v.FKField)
		} else {
			fmt.Fprintf(&b, "\t\t\tstale%s = existing.%s\n", v.PBGoName, v.FKField)
		}
	}
	if c := grp[0].Case; c != nil {
		if c.Column.Optional {
			fmt.Fprintf(&b, "\t\t\texisting.%s = nil\n", c.Field)
		} else {
			fmt.Fprintf(&b, "\t\t\texisting.%s = \"\"\n", c.Field)
		}
	}
	for _, v := range grp {
		frag := voWriteFragment(pkg, v, "merged", "existing")
		b.WriteString("\t\t\t" + strings.ReplaceAll(frag, "\n\t\t", "\n\t\t\t") + "\n")
	}
	b.WriteString("\t\t}")
	return b.String()
}

// voDeleteCleanup renders the removal of a VO row after its owner's delete,
// preserving the "no orphaned sub-rows" contract of the hand-written repos.
func voDeleteCleanup(pkg string, v voField) string {
	if v.Col.Optional {
		return fmt.Sprintf(
			"if existing.%s != nil {\n\t\t\tif err := tx.WithContext(ctx).Delete(&%s.%s{}, \"id = ?\", *existing.%s).Error; err != nil {\n\t\t\t\treturn err\n\t\t\t}\n\t\t}",
			v.FKField, voQual(pkg, v), v.Target.LocalName, v.FKField)
	}
	return fmt.Sprintf(
		"if existing.%s != \"\" {\n\t\t\tif err := tx.WithContext(ctx).Delete(&%s.%s{}, \"id = ?\", existing.%s).Error; err != nil {\n\t\t\t\treturn err\n\t\t\t}\n\t\t}",
		v.FKField, voQual(pkg, v), v.Target.LocalName, v.FKField)
}

// voStaleDelete renders the post-update removal of a replaced VO row.
func voStaleDelete(pkg string, v voField) string {
	return fmt.Sprintf(
		"if stale%s != \"\" {\n\t\t\tif err := tx.WithContext(ctx).Delete(&%s.%s{}, \"id = ?\", stale%s).Error; err != nil {\n\t\t\t\treturn err\n\t\t\t}\n\t\t}",
		v.PBGoName, voQual(pkg, v), v.Target.LocalName, v.PBGoName)
}
