// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// graph.go plans the value-object graphs that go deeper than the single-level
// path planVOFields covers: a value object with value objects of its own, a
// repeated message field stored as child rows (has-many), a child row that
// must point back at a row higher up the same write (an "owner" column, e.g. a
// time row carrying its event's id), and a value object keyed by a field of
// its own rather than a minted id (a natural key, e.g. an IANA time zone).
//
// The plan is a tree per repository resource. Every edge in it is one the
// adapters can write, read back, replace and delete without guessing: an edge
// whose subtree needs a column the writer cannot fill is dropped whole, so a
// resource never half-persists a field. Fields the single-level path already
// handles are left to it, keeping its output unchanged.

import (
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/target/gorm"
)

// maxGraphDepth bounds how far a value-object graph is followed. Deeper
// graphs exist only in pathological schemas; past it an edge stays Tier-2.
const maxGraphDepth = 4

// edgeKind distinguishes the two ways a value object hangs off its owner.
type edgeKind int

const (
	edgeOne  edgeKind = iota // singular field: FK on the owner points down at the child row
	edgeMany                 // repeated field: child rows point up at the owner
)

// gEdge is one value-object field of a graph node.
type gEdge struct {
	Kind      edgeKind
	FieldName string // proto field name, e.g. "available_periods"
	PBGoName  string // proto Go accessor base, e.g. "AvailablePeriods"
	Assoc     string // gorm association field on the owner model
	OrderCol  string // edgeMany: child column the rows are read back in
	// edgeOne only.
	FKCol   *schema.Column // FK column on the owner
	FKField string         // gorm FK field on the owner
	Natural bool           // child keyed by its own field: upserted, shared, never deleted with an owner
	Case    *voCase        // oneof discriminator on the owner, nil for a plain field
	Field   *protogen.Field

	Child *gNode
}

// gNode is one table in a write graph.
type gNode struct {
	Table   *schema.Table
	Schema  *schema.Schema
	Msg     *protogen.Message
	PKField string // gorm field of the key column
	PKCol   *schema.Column
	// NaturalPB is the proto Go accessor base of a natural key ("Id").
	NaturalPB string
	Edges     []*gEdge
	Owners    []gOwner
}

// gOwner is a column on a node that points at an ancestor row of the same
// write: filled with that ancestor's freshly minted key.
type gOwner struct {
	Field    string // gorm field
	Column   *schema.Column
	Depth    int // ancestor index in the path; 0 is the resource row
	Optional bool
}

// planGraph returns the deep value-object edges of r: those planVOFields left
// out and this planner can carry end to end.
func planGraph(pb *pbIndex, db *schema.Database, r *resource) []*gEdge {
	msg := pb.msgs[r.Table.Source.FullName()]
	if msg == nil {
		return nil
	}
	shallow := map[string]bool{}
	for _, v := range r.VOs {
		shallow[v.FieldName] = true
	}
	var out []*gEdge
	for _, e := range planEdges(pb, db, r.Schema, r.Table, msg, nil, r.ParentFK) {
		if !shallow[e.FieldName] {
			out = append(out, e)
		}
	}
	return out
}

// planEdges plans every value-object edge of table t (built from msg), which
// is reached along path (its ancestors, resource row first). skipFK is a
// column that is never an edge (the parent FK of a resource).
func planEdges(pb *pbIndex, db *schema.Database, s *schema.Schema, t *schema.Table, msg *protogen.Message, path []*schema.Table, skipFK string) []*gEdge {
	if len(path) >= maxGraphDepth {
		return nil
	}
	// The ancestors of t's children: t's own path, then t.
	down := append(path[:len(path):len(path)], t)
	byName := map[string]*protogen.Field{}
	for _, f := range msg.Fields {
		byName[string(f.Desc.Name())] = f
	}
	var out []*gEdge
	bts, _ := gorm.AssocPlan(db, s, t)
	for _, bt := range bts {
		c, tgt := bt.Col, bt.Target
		if c.Source != nil || c.Name == skipFK || tgt == nil || !tgt.ValueObject || tgt.Source == nil || inPath(down, tgt) {
			continue
		}
		name := naming.StripIDSuffix(c.Name)
		f := byName[name]
		if f == nil || f.Desc.IsList() || f.Message == nil || f.Message.Desc.FullName() != tgt.Source.FullName() {
			continue
		}
		natural := !hasGeneratedPK(tgt)
		child := planNode(pb, db, tgt, down, natural)
		if child == nil {
			continue
		}
		e := &gEdge{
			Kind:      edgeOne,
			FieldName: name,
			PBGoName:  f.GoName,
			Assoc:     bt.Field,
			FKCol:     c,
			FKField:   gormFieldName(c.Name, true),
			Natural:   natural,
			Field:     f,
			Child:     child,
		}
		if f.Oneof != nil && !f.Oneof.Desc.IsSynthetic() {
			cc := columnByName(t, string(f.Oneof.Desc.Name())+"_case")
			if cc == nil || cc.Enum == nil {
				continue
			}
			e.Case = &voCase{
				Column:      cc,
				Field:       gormFieldName(cc.Name, false),
				EnumLocal:   cc.Enum.LocalName,
				Const:       cc.Enum.LocalName + naming.PascalGo(name),
				OneofGoName: f.Oneof.GoName,
			}
		}
		out = append(out, e)
	}
	for _, hm := range gorm.HasManyEdges(db, s, t, msg) {
		if inPath(down, hm.Child) {
			continue
		}
		child := planNode(pb, db, hm.Child, down, false)
		if child == nil {
			continue
		}
		out = append(out, &gEdge{
			Kind:      edgeMany,
			FieldName: string(hm.PBField.Desc.Name()),
			PBGoName:  hm.PBField.GoName,
			Assoc:     hm.Field,
			OrderCol:  child.PKCol.Name,
			Field:     hm.PBField,
			Child:     child,
		})
	}
	return out
}

// planNode plans table t reached along path, or returns nil when the writer
// could not produce a valid row for it.
func planNode(pb *pbIndex, db *schema.Database, t *schema.Table, path []*schema.Table, natural bool) *gNode {
	msg := pb.msgs[t.Source.FullName()]
	s := schemaOf(db, t)
	if msg == nil || s == nil {
		return nil
	}
	n := &gNode{Table: t, Schema: s, Msg: msg}
	for _, c := range t.Columns {
		if c.Generated != "" || (natural && (c.PrimaryKey || c.Name == t.PKColumn)) {
			n.PKCol = c
		}
	}
	if n.PKCol == nil {
		return nil
	}
	n.PKField = gormFieldName(n.PKCol.Name, false)
	if natural {
		if n.PKCol.Source == nil {
			return nil
		}
		f := fieldOf(msg, n.PKCol.Source)
		if f == nil {
			return nil
		}
		n.NaturalPB = f.GoName
		// A shared, naturally keyed row is written as given; anything hanging
		// off it would be owned by every row that shares it.
		for _, fk := range t.ForeignKeys {
			if col := columnByName(t, fk.Column); col != nil && col.NotNull {
				return nil
			}
		}
		return n
	}
	n.Edges = planEdges(pb, db, s, t, msg, path, "")
	down := map[string]bool{}
	for _, e := range n.Edges {
		if e.Kind == edgeOne {
			down[e.FKCol.Name] = true
		}
	}
	for _, fk := range t.ForeignKeys {
		if down[fk.Column] {
			continue
		}
		col := columnByName(t, fk.Column)
		if col == nil {
			continue
		}
		depth := -1
		for i, a := range path {
			if fk.ReferencedProto != "" && a.Source != nil && string(a.Source.FullName()) == fk.ReferencedProto {
				depth = i
			}
		}
		if depth < 0 {
			if col.NotNull {
				return nil // a required reference nothing in this write can fill
			}
			continue
		}
		n.Owners = append(n.Owners, gOwner{
			Field:    gormFieldName(col.Name, true),
			Column:   col,
			Depth:    depth,
			Optional: col.Optional,
		})
	}
	// Resource references on a value object are names the converters leave
	// unmapped; a required one would be written empty.
	for _, c := range t.Columns {
		if c.FKModel != "" && c.Source != nil && c.NotNull {
			return nil
		}
	}
	return n
}

func inPath(path []*schema.Table, t *schema.Table) bool {
	for _, p := range path {
		if p == t {
			return true
		}
	}
	return false
}

func hasGeneratedPK(t *schema.Table) bool {
	for _, c := range t.Columns {
		if c.Generated != "" {
			return true
		}
	}
	return false
}

func schemaOf(db *schema.Database, t *schema.Table) *schema.Schema {
	for _, s := range db.Schemas {
		for _, x := range s.Tables {
			if x == t {
				return s
			}
		}
	}
	return nil
}

func fieldOf(msg *protogen.Message, fd protoreflect.FieldDescriptor) *protogen.Field {
	for _, f := range msg.Fields {
		if f.Desc.FullName() == fd.FullName() {
			return f
		}
	}
	return nil
}
