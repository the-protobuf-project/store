// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package prisma

// view.go prepares the fragment template view models: every naming and type
// decision happens here so the template stays purely presentational.

import (
	"sort"
	"strings"

	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
	"github.com/the-protobuf-project/store/plugin/factory/provenance"
	"github.com/the-protobuf-project/store/plugin/factory/target/searchindex"
	"github.com/the-protobuf-project/store/plugin/factory/target/types"
)

type fieldLine struct{ Doc, Decl string }

type modelView struct {
	Comment, Name, Map, Schema string
	Fields                     []fieldLine
	Indexes                    []fieldLine
}

// fragmentView assembles the data for one source proto's
// <file>.<provider>.prisma fragment. A fragment may span several @@schemas, so
// every model and enum carries its own schema (rendered per block in the
// template) rather than a single fragment-wide schema.
func fragmentView(db *schema.Database, g fragmentGroup, provider types.Provider, typeOf types.TypeOf, fx facets.Set) map[string]any {
	var enums []*schema.Enum
	for _, e := range g.enums {
		enums = append(enums, withFallbackComments(e))
	}
	dsName := naming.DatasourceName(db.Name)
	models := make([]modelView, 0, len(g.tables))
	for _, t := range g.tables {
		models = append(models, modelViewOf(t, provider, dsName, typeOf, fx, constraintPrefix(db, t)))
	}
	srcProto := g.sourceProto
	if srcProto == "" {
		srcProto = g.fileBase + ".proto"
	}
	return map[string]any{
		"Header": provenance.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Source:        srcProto,
			Database:      db.Name,
			SchemaLabel:   "schemas",
			Schema:        strings.Join(fragmentSchemas(g), ", "),
		}),
		"MultiSchema": provider == types.Postgres,
		"Enums":       enums,
		"Models":      models,
	}
}

// fragmentSchemas lists the distinct postgres schemas a fragment's models and
// enums belong to, in deterministic order, for the generated-file banner.
func fragmentSchemas(g fragmentGroup) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, t := range g.tables {
		add(t.PgSchema)
	}
	for _, e := range g.enums {
		add(e.PgSchema)
	}
	sort.Strings(out)
	return out
}

// constraintPrefix is the prefix t's constraint names take, or "" for Prisma's
// defaults. Prisma derives a primary key, index or unique constraint's name from
// the table alone (<table>_pkey, <table>_<cols>_idx) and requires every one to be
// unique across the database, while Postgres scopes them to a schema. So a table
// whose name another schema of the database also uses names its constraints
// explicitly, qualified by its schema ("<schema>_<table>"); every other table
// keeps the default names, so existing migrations see no change.
func constraintPrefix(db *schema.Database, t *schema.Table) string {
	n := 0
	for _, s := range db.Schemas {
		for _, o := range s.Tables {
			if o.Name == t.Name {
				n++
			}
		}
	}
	if n < 2 || t.PgSchema == "" {
		return ""
	}
	return t.PgSchema + "_" + t.Name
}

// modelViewOf renders one table into template-ready field and index lines.
// dsName is the Prisma datasource block name, used to prefix native-type
// attributes; prefix, when set, qualifies the table's constraint names
// (constraintPrefix, namer).
func modelViewOf(t *schema.Table, provider types.Provider, dsName string, typeOf types.TypeOf, fx facets.Set, prefix string) modelView {
	names := namer{table: t.Name, prefix: prefix}
	fkByCol := map[string]*schema.ForeignKey{}
	for _, fk := range t.ForeignKeys {
		fkByCol[fk.Column] = fk
	}

	m := modelView{Comment: commentOr(t.Comment, t.ModelName+" model."), Name: t.ModelName, Map: t.Name, Schema: t.PgSchema}

	// Reserve every scalar field name so a relation field can't collide with one.
	// colByName lets index emission resolve a DB column to its scalar field name.
	used := map[string]bool{}
	colByName := map[string]*schema.Column{}
	for _, col := range t.Columns {
		used[scalarFieldName(col)] = true
		colByName[col.Name] = col
	}

	for _, col := range t.Columns {
		m.Fields = append(m.Fields, fieldLine{Doc: fieldDoc(col), Decl: fieldDecl(col, provider, dsName, typeOf, names)})

		// BelongsTo relation — emitted immediately after the FK column. The field
		// name is derived from the FK column (minus _id) so two FKs to the same
		// model stay distinct (organizer / creator), with a named @relation when
		// the model pair is ambiguous.
		if fk, ok := fkByCol[col.Name]; ok {
			relType := fk.ReferencedModel
			if col.Optional {
				relType += "?"
			}
			args := ""
			if fk.RelationName != "" {
				args = `"` + fk.RelationName + `", `
			}
			args += "fields: [" + scalarFieldName(col) + "], references: [" +
				naming.Camel(fk.ReferencedColumn) + "]"
			if a := prismaAction(fk.OnDelete); a != "" {
				args += ", onDelete: " + a
			}
			if a := prismaAction(fk.OnUpdate); a != "" {
				args += ", onUpdate: " + a
			}
			if name, ok := names.name([]string{col.Name}, "_fkey"); ok {
				args += `, map: "` + name + `"`
			}
			field := naming.Unique(naming.Camel(naming.StripIDSuffix(col.Name)), used)
			m.Fields = append(m.Fields, fieldLine{
				Doc:  "Relation to " + fk.ReferencedModel + " via " + col.Name + ".",
				Decl: field + " " + relType + " @relation(" + args + ")",
			})
		}
	}

	// HasMany back-references (both sides required by Prisma's relation validator).
	for _, hm := range t.HasMany {
		field := naming.Unique(naming.Camel(hm.Field), used)
		decl := field + " " + hm.Model + "[]"
		if hm.RelationName != "" {
			decl += ` @relation("` + hm.RelationName + `")`
		}
		m.Fields = append(m.Fields, fieldLine{
			Doc:  "Back-relation: " + hm.Model + " records that reference this model via " + hm.ViaFK + ".",
			Decl: decl,
		})
	}

	for _, idx := range t.Indexes {
		// Prisma's @@index/@@unique must name the Prisma *scalar field*, not the DB
		// column. For FK columns those differ (column "resource" → field
		// "resourceID"), and the bare camel name would collide with the relation
		// field — so resolve through scalarFieldName when the column is known.
		cols := make([]string, len(idx.Columns))
		for i, c := range idx.Columns {
			if col, ok := colByName[c]; ok {
				cols[i] = scalarFieldName(col)
			} else {
				cols[i] = naming.Camel(c)
			}
		}
		directive, label, suffix := "@@index", "Index", "_idx"
		if len(idx.Columns) > 1 {
			label = "Composite index"
		}
		if idx.Unique {
			directive, label, suffix = "@@unique", "Unique constraint", "_key"
		}
		mapArg := ""
		if name, ok := names.name(idx.Columns, suffix); ok {
			mapArg = `, map: "` + name + `"`
		}
		m.Indexes = append(m.Indexes, fieldLine{
			Doc:  label + " on [" + strings.Join(idx.Columns, ", ") + "].",
			Decl: directive + "([" + strings.Join(cols, ", ") + "]" + mapArg + ")",
		})
	}

	// GIN indexes serving the query surface (see the searchindex package). The
	// operator class rides on the field via ops:, and raw() passes it through
	// verbatim — Prisma has no named constant for gin_trgm_ops, which comes from
	// an extension rather than core PostgreSQL.
	for _, p := range searchindex.For(t, fx) {
		if p.Expression != "" {
			// Prisma cannot declare an expression index; the SQL target and
			// the GORM registry create it.
			continue
		}
		if col, ok := colByName[p.Column]; ok && strings.HasPrefix(typeOf(col), "TSTZ") {
			// A range column is Unsupported(...) to Prisma, which cannot index
			// it; the SQL target and the GORM registry create the GiST index.
			continue
		}
		field := naming.Camel(p.Column)
		if col, ok := colByName[p.Column]; ok {
			field = scalarFieldName(col)
		}
		if p.Ops != "" {
			field += `(ops: raw("` + p.Ops + `"))`
		}
		kind := "Gin"
		if p.MethodOf() == searchindex.MethodGiST {
			kind = "Gist"
		}
		m.Indexes = append(m.Indexes, fieldLine{
			Doc:  strings.ToUpper(p.MethodOf()) + " index: " + p.Why + ".",
			Decl: "@@index([" + field + "], type: " + kind + ")",
		})
	}
	return m
}
