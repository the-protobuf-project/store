// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// joins.go plans the repeated references a resource keeps in join tables: a
// repeated google.api.resource_reference whose target is generated in the
// same run becomes a join table (protokit's schema.Table.Join), and the
// adapters write one row per element on create and update, read them back
// into the field on every read, and filter on them through the join.

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
)

// joinField is one repeated reference of a resource kept in a join table.
type joinField struct {
	Join   *schema.Join
	Table  *schema.Table  // the join table
	Schema *schema.Schema // its schema
	// TargetTable and TargetSchema hold the referenced records; Target is
	// their resource, nil when the target has no generated repository.
	TargetTable  *schema.Table
	TargetSchema *schema.Schema
	Target       *resource
	GoField      string // the proto Go field, e.g. "Spaces"
}

// planJoins returns the join-table fields of r, in the order the join tables
// were built (field order).
func planJoins(pb *pbIndex, db *schema.Database, resources map[*schema.Table]*resource, r *resource) []joinField {
	var out []joinField
	for _, s := range db.Schemas {
		for _, t := range s.Tables {
			j := t.Join
			if j == nil || j.OwnerProto != r.Table.ProtoMessage {
				continue
			}
			jf := joinField{Join: j, Table: t, Schema: s, GoField: naming.PascalGo(j.Field)}
			if msg := pb.msgs[r.Table.Source.FullName()]; msg != nil {
				for _, f := range msg.Fields {
					if string(f.Desc.Name()) == j.Field {
						jf.GoField = f.GoName
					}
				}
			}
			for _, ts := range db.Schemas {
				for _, tt := range ts.Tables {
					if tt.Source != nil && tt.ProtoMessage == j.TargetProto && !tt.ValueObject {
						jf.TargetTable, jf.TargetSchema, jf.Target = tt, ts, resources[tt]
					}
				}
			}
			if jf.TargetTable == nil {
				continue // the target is not stored here: nothing to key the rows by
			}
			out = append(out, jf)
		}
	}
	return out
}

// targetPK is the target table's key column.
func (j joinField) targetPK() string {
	if j.Target != nil && j.Target.Cols.PK != nil {
		return j.Target.Cols.PK.Name
	}
	if j.TargetTable.PKColumn != "" {
		return j.TargetTable.PKColumn
	}
	return "id"
}

// targetName is the target's unique name column when its rows are found by
// name (a nested resource keyed by a surrogate), "" when the last id of a
// name is the key.
func (j joinField) targetName() string {
	if j.Target != nil && j.Target.nameKeyed() {
		return j.Target.Cols.NameKey.Name
	}
	return ""
}

// collection is the target's collection when a top-level target's name is
// rebuilt from its key ("shelves"), "" otherwise.
func (j joinField) collection() string {
	if j.Join.NameColumn != "" || j.Target == nil || len(j.Target.Segments) != 1 {
		return ""
	}
	return j.Target.Segments[0].Collection
}

// joinKey is the join table's own key column.
func (j joinField) joinKey() string {
	if j.Table.PKColumn != "" {
		return j.Table.PKColumn
	}
	return "id"
}

// gormLiteral renders the repox.Join value the GORM adapter writes and reads
// the field through.
func (j joinField) gormLiteral() string {
	return fmt.Sprintf("repox.Join{Field: %q, Table: `%s`, Key: %q, Owner: %q, Target: %q, Name: %q, TargetTable: `%s`, TargetKey: %q, TargetName: %q, Collection: %q}",
		j.Join.Field, qualifiedIdent(j.Schema.Name, j.Table.Name), j.joinKey(), j.Join.OwnerColumn, j.Join.TargetColumn, j.Join.NameColumn,
		qualifiedIdent(j.TargetSchema.Name, j.TargetTable.Name), j.targetPK(), j.targetName(), j.collection())
}

// joinView is the template data of one join-table field.
type joinView struct {
	Field   string // proto field, e.g. "spaces"
	GoField string // proto Go field, e.g. "Spaces"
	Var     string // the GORM adapter's repox.Join variable
	Literal string // its value

	// GraphQL: the join table's client names and columns.
	Domain, Resource, ResPkg string
	DomPkg                   string // the client's schema package, e.g. "catalogql"
	InsertResp, DeleteResp   string
	KeyField                 string // the join row's key field, e.g. "Id"
	OwnerField               string // e.g. "FixedPackageId"
	TargetField              string // e.g. "SpaceId"
	NameField                string // e.g. "SpaceName"; "" when the name is rebuilt from the key
	Collection               string // see joinField.collection
	// Lookup finds a target row's key by its name (a nested target keyed by
	// a surrogate); nil when the last id of a name is the key.
	Lookup *gqlParentLookup
}

// joinViews renders r's join fields for the templates.
func joinViews(r *resource) []joinView {
	out := make([]joinView, 0, len(r.Joins))
	for _, j := range r.Joins {
		nm := gqlNamesOf(j.Table)
		v := joinView{
			Field:       j.Join.Field,
			GoField:     j.GoField,
			Var:         naming.CamelFirst(r.Table.LocalName) + j.GoField + "Join",
			Literal:     j.gormLiteral(),
			Domain:      nm.Domain,
			Resource:    nm.Resource,
			ResPkg:      nm.ResPkg,
			DomPkg:      nm.DomPkg,
			InsertResp:  nm.InsertResp,
			DeleteResp:  nm.DeleteResp,
			KeyField:    export(camel(j.joinKey())),
			OwnerField:  export(camel(j.Join.OwnerColumn)),
			TargetField: export(camel(j.Join.TargetColumn)),
			Collection:  j.collection(),
		}
		if j.Join.NameColumn != "" {
			v.NameField = export(camel(j.Join.NameColumn))
		}
		if n := j.targetName(); n != "" {
			lk := parentLookupOf(j.TargetTable, n, j.targetPK())
			v.Lookup = &lk
		}
		out = append(out, v)
	}
	return out
}

// isJoinTable reports whether t is a synthesized many-to-many join table.
func isJoinTable(t *schema.Table) bool { return t != nil && t.Join != nil }

// joinOwnerFK reports whether fk is a join table's key to its owner: the
// join's rows are the owner's own, not records referring to it.
func joinOwnerFK(t *schema.Table, fk *schema.ForeignKey) bool {
	return isJoinTable(t) && strings.EqualFold(fk.Column, t.Join.OwnerColumn)
}
