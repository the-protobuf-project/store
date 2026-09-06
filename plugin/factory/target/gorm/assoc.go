// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package gorm

// assoc.go plans the association fields a model carries (belongs-to and
// has-many back-references), in declaration order. models.go rendering and the
// converters target both consume this plan, so the Go field names they use can
// never drift apart.

import (
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
)

// belongsTo is one emitted belongs-to association field.
type BelongsTo struct {
	Col      *schema.Column // the FK column the association rides on
	Field    string         // Go field name on the owning struct
	Target   *schema.Table  // referenced table
	CrossPkg string         // "" when same-schema; else the target's Go package name
}

// hasManyField is one emitted has-many back-reference field.
type hasManyField struct {
	Ref   *schema.HasManyRef
	Field string
}

// assocPlan computes the association fields for table t exactly as models.go
// emits them: a belongs-to field alongside each FK column whose target is in
// the same schema (or is a cross-schema value object, when safe — see
// emitCrossAssoc), then the same-schema has-many back-references. Field names
// are deduplicated against the scalar columns in declaration order.
func AssocPlan(db *schema.Database, s *schema.Schema, t *schema.Table) ([]BelongsTo, []hasManyField) {
	inThisSchema := modelSchemaSet(db, s.Name)
	tableByModel := tableByModelFunc(db)
	voEdges := crossSchemaVOEdges(db)

	used := map[string]bool{}
	for _, col := range t.Columns {
		used[gormFieldName(col)] = true
	}
	var bts []BelongsTo
	for _, col := range t.Columns {
		if col.FKModel == "" {
			continue
		}
		if inThisSchema(col.FKModel) {
			bts = append(bts, BelongsTo{
				Col:    col,
				Field:  naming.Unique(naming.PascalGo(naming.StripIDSuffix(col.Name)), used),
				Target: tableByModel(col.FKModel),
			})
		} else if tgt := tableByModel(col.FKModel); tgt != nil && tgt.ValueObject &&
			dbGoModule(db) != "" && emitCrossAssoc(s.Name, tgt.PgSchema, voEdges) {
			bts = append(bts, BelongsTo{
				Col:      col,
				Field:    naming.Unique(naming.PascalGo(naming.StripIDSuffix(col.Name)), used),
				Target:   tgt,
				CrossPkg: naming.GoPackage(tgt.PgSchema),
			})
		}
	}
	var hms []hasManyField
	for _, hm := range t.HasMany {
		if !inThisSchema(hm.Model) {
			continue
		}
		hms = append(hms, hasManyField{Ref: hm, Field: naming.Unique(naming.PascalGo(hm.Field), used)})
	}
	return bts, hms
}
