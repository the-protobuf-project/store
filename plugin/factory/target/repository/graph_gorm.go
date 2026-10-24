// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// graph_gorm.go renders the GORM adapter fragments for the deep value-object
// graphs planned in graph.go. The fragments splice into the same template
// slots the single-level VO path uses, plus an after-insert slot for has-many
// children (which point at the resource row, so must follow it).
//
// Write order inside the transaction, at every level: a node's singular value
// objects first (it points at them), then the node, then its has-many
// children (they point at it). Every key is minted before any insert, so an
// owner column -- a child pointing at an ancestor of the same write -- is
// filled from a key that already exists in Go even where its row is inserted
// later. The output is gofmt'd, so fragments only need to be valid Go.

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/naming"
)

// graphGorm is the per-resource bundle of rendered deep-graph fragments.
type graphGorm struct {
	BeforeCreates  []string // singular value objects, before the resource insert
	AfterCreates   []string // has-many children, after the resource insert
	Preloads       string   // .Preload chain for every node of the graph
	ReadPreloads   []readPreload
	StaleVars      []string // stale-subtree captures, top of the update transaction
	Updates        []string // masked subtree replacement, before the row update
	Detach         []string // association fields cleared so Save writes only the row
	StaleDels      []string // stale-subtree removal, after the row update
	DeleteCleanups []string // subtree removal after the resource delete
	MaskLines      []string
	MaskOneofs     map[string]int // oneof Go name -> index in MaskLines
	CrossPkgs      []string
	NeedsClause    bool // a natural-key upsert uses gorm.io/gorm/clause
}

// gormGraphRenderer carries naming state across one resource's fragments.
type gormGraphRenderer struct {
	r       *resource
	pkg     string // the resource schema's gorm models qualifier
	n       int    // fresh-variable counter
	cross   map[string]bool
	out     *graphGorm
	preload []string
}

// graphGormFragments renders every deep-graph fragment for r.
func graphGormFragments(pkg string, r *resource) graphGorm {
	var out graphGorm
	if len(r.Graph) == 0 {
		return out
	}
	g := &gormGraphRenderer{r: r, pkg: pkg, cross: map[string]bool{}, out: &out}
	for _, e := range r.Graph {
		from := len(g.preload)
		g.preloads(e, "")
		out.ReadPreloads = append(out.ReadPreloads, readPreload{Field: e.FieldName, Chain: strings.Join(g.preload[from:], "")})
	}
	out.Preloads = strings.Join(g.preload, "")

	for _, e := range r.Graph {
		if e.Kind == edgeOne {
			out.BeforeCreates = append(out.BeforeCreates, g.createOne(e, "in", "m", pkg, []string{"id"}))
		} else {
			out.AfterCreates = append(out.AfterCreates, g.createMany(e, "in", []string{"id"}))
		}
		out.DeleteCleanups = append(out.DeleteCleanups, g.deleteEdge(e, "existing"))
	}

	// Updates replace a touched field's whole subtree. Oneof members that are
	// value objects share one group, like the single-level path.
	for _, grp := range graphGroups(r.Graph) {
		var fields []string
		for _, e := range grp {
			fields = append(fields, e.FieldName)
		}
		conds := []string{touchedAny(fields, grp[0].Case)}
		var b strings.Builder
		fmt.Fprintf(&b, "if %s {\n", conds[0])
		for _, e := range grp {
			stale := "stale" + e.PBGoName
			if e.Kind == edgeOne {
				out.StaleVars = append(out.StaleVars, fmt.Sprintf("var %s *%s.%s", stale, g.qual(e.Child), e.Child.Table.LocalName))
				fmt.Fprintf(&b, "%s = existing.%s\n", stale, e.Assoc)
				if e.FKCol.Optional {
					fmt.Fprintf(&b, "existing.%s = nil\n", e.FKField)
				} else {
					fmt.Fprintf(&b, "existing.%s = \"\"\n", e.FKField)
				}
				out.StaleDels = append(out.StaleDels, g.deleteOneVar(e, stale))
			} else {
				out.StaleVars = append(out.StaleVars, fmt.Sprintf("var %s []%s.%s", stale, g.qual(e.Child), e.Child.Table.LocalName))
				fmt.Fprintf(&b, "%s = existing.%s\n", stale, e.Assoc)
				out.StaleDels = append(out.StaleDels, g.deleteManyVar(e, stale))
			}
		}
		// The discriminator is not reset here: the row's mutable write-back
		// already set it from the merged proto, whichever arm it carries.
		for _, e := range grp {
			if e.Kind == edgeOne {
				b.WriteString(g.createOne(e, "merged", "existing", g.pkg, []string{"id"}) + "\n")
			} else {
				b.WriteString(g.createMany(e, "merged", []string{"id"}) + "\n")
			}
		}
		b.WriteString("}")
		out.Updates = append(out.Updates, b.String())

		if c := grp[0].Case; c != nil {
			if out.MaskOneofs == nil {
				out.MaskOneofs = map[string]int{}
			}
			out.MaskOneofs[c.OneofGoName] = len(out.MaskLines)
			out.MaskLines = append(out.MaskLines, fmt.Sprintf(
				"if %s {\n\tmerged.%s = in.%s\n}", strings.Join(conds, " || "), c.OneofGoName, c.OneofGoName))
		} else {
			out.MaskLines = append(out.MaskLines, fmt.Sprintf(
				"if repox.GroupTouched(paths, %q) {\n\tmerged.%s = in.Get%s()\n}", grp[0].FieldName, grp[0].PBGoName, grp[0].PBGoName))
		}
	}
	// Clear every graph association before the row is saved: GORM's Save
	// upserts loaded associations, which would write a replaced subtree back.
	for _, e := range r.Graph {
		out.Detach = append(out.Detach, fmt.Sprintf("existing.%s = nil", e.Assoc))
	}
	for p := range g.cross {
		out.CrossPkgs = append(out.CrossPkgs, p)
	}
	return out
}

// graphGroups buckets edges so each real oneof forms one group.
func graphGroups(es []*gEdge) [][]*gEdge {
	var groups [][]*gEdge
	byOneof := map[string]int{}
	for _, e := range es {
		if e.Case == nil {
			groups = append(groups, []*gEdge{e})
			continue
		}
		if i, ok := byOneof[e.Case.OneofGoName]; ok {
			groups[i] = append(groups[i], e)
			continue
		}
		byOneof[e.Case.OneofGoName] = len(groups)
		groups = append(groups, []*gEdge{e})
	}
	return groups
}

// qual is the gorm package qualifier for a node's model.
func (g *gormGraphRenderer) qual(n *gNode) string {
	if n.Schema == g.r.Schema {
		return g.pkg
	}
	p := naming.GoPackage(n.Table.PgSchema)
	g.cross[p] = true
	return p
}

func (g *gormGraphRenderer) fresh(prefix string) string {
	g.n++
	return fmt.Sprintf("%s%d", prefix, g.n)
}

// preloads appends the Preload calls for e's subtree under path prefix.
// Has-many paths read back in key order: keys are ULIDs minted in write
// order, so a list comes back as it was written.
func (g *gormGraphRenderer) preloads(e *gEdge, prefix string) {
	p := e.Assoc
	if prefix != "" {
		p = prefix + "." + e.Assoc
	}
	if e.Kind == edgeMany {
		g.preload = append(g.preload, fmt.Sprintf(".Preload(%q, func(db *gorm.DB) *gorm.DB { return db.Order(%q) })", p, e.OrderCol))
	} else {
		g.preload = append(g.preload, fmt.Sprintf(".Preload(%q)", p))
	}
	for _, c := range e.Child.Edges {
		g.preloads(c, p)
	}
}

// fill renders the key and owner assignments of a freshly built node row.
func (g *gormGraphRenderer) fill(n *gNode, row, src string, ancestors []string) string {
	var b strings.Builder
	if n.NaturalPB != "" {
		fmt.Fprintf(&b, "%s.%s = %s.Get%s()\n", row, n.PKField, src, n.NaturalPB)
	} else {
		fmt.Fprintf(&b, "%s.%s = repox.NewULID()\n", row, n.PKField)
	}
	for _, o := range n.Owners {
		if o.Optional {
			fmt.Fprintf(&b, "%s.%s = repox.Ptr(%s)\n", row, o.Field, ancestors[o.Depth])
		} else {
			fmt.Fprintf(&b, "%s.%s = %s\n", row, o.Field, ancestors[o.Depth])
		}
	}
	return b.String()
}

// insert renders the node row's insert. A naturally keyed row is shared, so
// it is written only if absent.
func (g *gormGraphRenderer) insert(n *gNode, row string) string {
	if n.NaturalPB != "" {
		g.out.NeedsClause = true
		return fmt.Sprintf("if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(%s).Error; err != nil {\nreturn err\n}\n", row)
	}
	return fmt.Sprintf("if err := tx.WithContext(ctx).Omit(clause.Associations).Create(%s).Error; err != nil {\nreturn err\n}\n", row)
}

// createOne renders "when the singular field is set, write its subtree and
// point the owner row at it".
func (g *gormGraphRenderer) createOne(e *gEdge, src, owner, ownerQual string, ancestors []string) string {
	g.out.NeedsClause = true
	v, row := g.fresh("v"), g.fresh("n")
	q := g.qual(e.Child)
	var b strings.Builder
	fmt.Fprintf(&b, "if %s := %s.Get%s(); %s != nil {\n", v, src, e.PBGoName, v)
	if e.Natural {
		fmt.Fprintf(&b, "if %s.Get%s() != \"\" {\n", v, e.Child.NaturalPB)
	}
	fmt.Fprintf(&b, "%s := %s.%sFromProto(%s)\n", row, q, e.Child.Table.LocalName, v)
	b.WriteString(g.fill(e.Child, row, v, ancestors))
	next := append(ancestors[:len(ancestors):len(ancestors)], row+"."+e.Child.PKField)
	for _, c := range e.Child.Edges {
		if c.Kind == edgeOne {
			b.WriteString(g.createOne(c, v, row, q, next) + "\n")
		}
	}
	b.WriteString(g.insert(e.Child, row))
	for _, c := range e.Child.Edges {
		if c.Kind == edgeMany {
			b.WriteString(g.createMany(c, v, next) + "\n")
		}
	}
	if e.FKCol.Optional {
		fmt.Fprintf(&b, "%s.%s = repox.Ptr(%s.%s)\n", owner, e.FKField, row, e.Child.PKField)
	} else {
		fmt.Fprintf(&b, "%s.%s = %s.%s\n", owner, e.FKField, row, e.Child.PKField)
	}
	if e.Case != nil {
		fmt.Fprintf(&b, "c := %s.%s\n", ownerQual, e.Case.Const)
		if e.Case.Column.Optional {
			fmt.Fprintf(&b, "%s.%s = &c\n", owner, e.Case.Field)
		} else {
			fmt.Fprintf(&b, "%s.%s = c\n", owner, e.Case.Field)
		}
	}
	if e.Natural {
		b.WriteString("}\n")
	}
	b.WriteString("}")
	return b.String()
}

// createMany renders "write one child subtree per element, in order".
func (g *gormGraphRenderer) createMany(e *gEdge, src string, ancestors []string) string {
	g.out.NeedsClause = true
	v, row := g.fresh("v"), g.fresh("n")
	q := g.qual(e.Child)
	var b strings.Builder
	fmt.Fprintf(&b, "for _, %s := range %s.Get%s() {\n", v, src, e.PBGoName)
	fmt.Fprintf(&b, "%s := %s.%sFromProto(%s)\n", row, q, e.Child.Table.LocalName, v)
	b.WriteString(g.fill(e.Child, row, v, ancestors))
	next := append(ancestors[:len(ancestors):len(ancestors)], row+"."+e.Child.PKField)
	for _, c := range e.Child.Edges {
		if c.Kind == edgeOne {
			b.WriteString(g.createOne(c, v, row, q, next) + "\n")
		}
	}
	b.WriteString(g.insert(e.Child, row))
	for _, c := range e.Child.Edges {
		if c.Kind == edgeMany {
			b.WriteString(g.createMany(c, v, next) + "\n")
		}
	}
	b.WriteString("}")
	return b.String()
}

// deleteEdge renders the removal of e's loaded subtree off owner.
func (g *gormGraphRenderer) deleteEdge(e *gEdge, owner string) string {
	if e.Kind == edgeOne {
		return g.deleteOneVar(e, owner+"."+e.Assoc)
	}
	return g.deleteManyVar(e, owner+"."+e.Assoc)
}

// deleteOneVar removes the subtree rooted at the loaded pointer expr: its
// has-many children first (they point at it), then it, then its singular value
// objects (it pointed at them). Natural-key rows are shared and stay.
func (g *gormGraphRenderer) deleteOneVar(e *gEdge, expr string) string {
	d := g.fresh("d")
	var b strings.Builder
	fmt.Fprintf(&b, "if %s := %s; %s != nil {\n", d, expr, d)
	b.WriteString(g.deleteNode(e.Child, d))
	b.WriteString("}")
	return b.String()
}

// deleteManyVar removes every loaded child subtree in the slice expr.
func (g *gormGraphRenderer) deleteManyVar(e *gEdge, expr string) string {
	i, d := g.fresh("i"), g.fresh("d")
	var b strings.Builder
	fmt.Fprintf(&b, "for %s := range %s {\n%s := &%s[%s]\n", i, expr, d, expr, i)
	b.WriteString(g.deleteNode(e.Child, d))
	b.WriteString("}")
	return b.String()
}

func (g *gormGraphRenderer) deleteNode(n *gNode, d string) string {
	if n.NaturalPB != "" {
		return "_ = " + d + "\n"
	}
	var b strings.Builder
	for _, c := range n.Edges {
		if c.Kind == edgeMany {
			b.WriteString(g.deleteManyVar(c, d+"."+c.Assoc) + "\n")
		}
	}
	fmt.Fprintf(&b, "if err := tx.WithContext(ctx).Delete(&%s.%s{}, %q, %s.%s).Error; err != nil {\nreturn err\n}\n",
		g.qual(n), n.Table.LocalName, n.PKCol.Name+" = ?", d, n.PKField)
	for _, c := range n.Edges {
		if c.Kind == edgeOne {
			b.WriteString(g.deleteOneVar(c, d+"."+c.Assoc) + "\n")
		}
	}
	return b.String()
}
