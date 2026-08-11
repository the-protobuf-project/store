package prisma

// view.go prepares the fragment template view models: every naming and type
// decision happens here so the template stays purely presentational.

import (
	"sort"
	"strings"

	"github.com/the-protobuf-project/orm/plugin/factory/target/types"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
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
func fragmentView(db *schema.Database, g fragmentGroup, provider types.Provider, typeOf types.TypeOf) map[string]any {
	var enums []*schema.Enum
	for _, e := range g.enums {
		enums = append(enums, withFallbackComments(e))
	}
	dsName := naming.DatasourceName(db.Name)
	models := make([]modelView, 0, len(g.tables))
	for _, t := range g.tables {
		models = append(models, modelViewOf(t, provider, dsName, typeOf))
	}
	srcProto := g.sourceProto
	if srcProto == "" {
		srcProto = g.fileBase + ".proto"
	}
	return map[string]any{
		"Header": header.Render("//", header.Info{
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

// modelViewOf renders one table into template-ready field and index lines.
// dsName is the Prisma datasource block name, used to prefix native-type attributes.
func modelViewOf(t *schema.Table, provider types.Provider, dsName string, typeOf types.TypeOf) modelView {
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
		m.Fields = append(m.Fields, fieldLine{Doc: fieldDoc(col), Decl: fieldDecl(col, provider, dsName, typeOf)})

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
		directive, label := "@@index", "Composite index"
		if idx.Unique {
			directive, label = "@@unique", "Unique constraint"
		}
		m.Indexes = append(m.Indexes, fieldLine{
			Doc:  label + " on [" + strings.Join(idx.Columns, ", ") + "].",
			Decl: directive + "([" + strings.Join(cols, ", ") + "])",
		})
	}
	return m
}
