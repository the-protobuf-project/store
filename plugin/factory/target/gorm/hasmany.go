// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package gorm

// hasmany.go decides which has-many back-references are a repeated message
// field the generated code can map in both directions: the converters render
// them ToProto, and the repository target writes, preloads and replaces them.
// Both consume this one decision, so a field is never readable by one and
// silently dropped by the other.

import (
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/protokit/schema"
)

// HasManyEdge is one repeated value-object field stored as child rows that
// carry an FK back to the parent row.
type HasManyEdge struct {
	// Field is the has-many association field on the parent model, e.g.
	// "AvailablePeriods".
	Field string
	// PBField is the repeated proto field it materializes.
	PBField *protogen.Field
	// Child is the child value-object table.
	Child *schema.Table
	// ViaFK is the FK column on the child pointing at the parent, e.g.
	// "availability_id".
	ViaFK string
}

// HasManyEdges returns the has-many edges of t (built from msg) that are
// unambiguous enough to generate. An edge is generated only when every row
// in the child table provably belongs to this field:
//
//   - the child is a keyed value object (no resource of its own);
//   - msg has exactly one repeated field of the child's message type, so rows
//     never need sorting between two lists stored in one table;
//   - the child table has exactly one owner across the database, so rows
//     written for another parent table never appear here;
//   - no table references the child as a belongs-to value object, so the
//     child table never also holds rows owned singly (e.g. an event's start
//     time living in the table its excluded times are stored in).
//
// Anything else stays with Tier-2 overrides, as before.
func HasManyEdges(db *schema.Database, s *schema.Schema, t *schema.Table, msg *protogen.Message) []HasManyEdge {
	if msg == nil {
		return nil
	}
	_, hms := AssocPlan(db, s, t)
	if len(hms) == 0 {
		return nil
	}
	tableByModel := tableByModelFunc(db)
	var out []HasManyEdge
	for _, hm := range hms {
		child := tableByModel(hm.Ref.Model)
		if child == nil || !child.ValueObject || child.Source == nil || !hasGeneratedPK(child) {
			continue
		}
		var match *protogen.Field
		n := 0
		for _, f := range msg.Fields {
			if f.Desc.IsList() && f.Message != nil && f.Message.Desc.FullName() == child.Source.FullName() {
				match = f
				n++
			}
		}
		if n != 1 || hm.Ref.RelationName != "" {
			continue
		}
		if owners(db, child) != 1 || isBelongsToTarget(db, child) {
			continue
		}
		out = append(out, HasManyEdge{Field: hm.Field, PBField: match, Child: child, ViaFK: hm.Ref.ViaFK})
	}
	return out
}

func hasGeneratedPK(t *schema.Table) bool {
	for _, c := range t.Columns {
		if c.Generated != "" {
			return true
		}
	}
	return false
}

// owners counts the repeated fields of child's message type across every
// table's message: exactly one means every child row belongs to one list.
func owners(db *schema.Database, child *schema.Table) int {
	n := 0
	forEachField(db, child, func(fd protoreflect.FieldDescriptor) {
		if fd.IsList() {
			n++
		}
	})
	return n
}

// isBelongsToTarget reports whether any message holds child's type in a
// singular field — a singly-owned value object whose rows would share the
// child table with the list's.
//
// Decided off the proto, not the IR's relations: protokit records a
// back-relation for every foreign key in both directions, so the relation
// list alone cannot tell "owned once" from "owned as a list".
func isBelongsToTarget(db *schema.Database, child *schema.Table) bool {
	single := false
	forEachField(db, child, func(fd protoreflect.FieldDescriptor) {
		if !fd.IsList() {
			single = true
		}
	})
	return single
}

// forEachField calls fn for every message-typed field, on any table's
// message, whose type is child's message.
func forEachField(db *schema.Database, child *schema.Table, fn func(protoreflect.FieldDescriptor)) {
	if child.Source == nil {
		return
	}
	want := child.Source.FullName()
	seen := map[protoreflect.FullName]bool{}
	for _, s := range db.Schemas {
		for _, t := range s.Tables {
			if t.Source == nil || seen[t.Source.FullName()] {
				continue
			}
			seen[t.Source.FullName()] = true
			fields := t.Source.Fields()
			for i := 0; i < fields.Len(); i++ {
				fd := fields.Get(i)
				if fd.Message() != nil && fd.Message().FullName() == want {
					fn(fd)
				}
			}
		}
	}
}
