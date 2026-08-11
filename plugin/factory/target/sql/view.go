package sql

// view.go prepares the schema.sql template view: column definitions, enum
// CREATE TYPE data, FK constraints, and index statements.

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/provenance"
	"github.com/the-protobuf-project/store/plugin/factory/target/types"
)

// quoteIdent wraps a SQL identifier in double quotes (doubling any embedded
// quote) so table/column/type names that collide with reserved words — order,
// user, window — emit valid DDL instead of silently breaking.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// quoteLiteral wraps a SQL string literal in single quotes, doubling any
// embedded single quote so apostrophes in enum values can't terminate the
// literal. (Column default_value is intentionally not passed through here: it
// is documented as a raw SQL expression, e.g. now(), not a string literal.)
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// qualified joins a schema and object name into a quoted, schema-qualified
// reference: ("kitchen", "sinks") → "kitchen"."sinks".
func qualified(schema, name string) string {
	return quoteIdent(schema) + "." + quoteIdent(name)
}

type itemView struct {
	Comment, Def string
	Last         bool
}

type enumDDLView struct {
	Comment, TypeRef, ValueList string
}

type tableView struct {
	Comment, Ref string
	Items        []itemView
	Indexes      []string
}

// schemaDDL holds the per-schema statement groups shared by the per-schema file
// and the consolidated migrate.sql: trigger functions, triggers, and COMMENT ON.
type schemaDDL struct {
	Functions []string // CREATE OR REPLACE FUNCTION for auto-update columns
	Triggers  []string // CREATE TRIGGER firing those functions on UPDATE
	Comments  []string // COMMENT ON TABLE / COLUMN for documented objects
}

// schemaView assembles the template data for one schema's DDL file.
func schemaView(db *schema.Database, s *schema.Schema, typeOf types.TypeOf) map[string]any {
	enums := enumDDLViews(s)

	var tables []tableView
	for _, t := range s.Tables {
		tables = append(tables, tableViewOf(s, t, typeOf))
	}

	ddl := schemaDDLOf(s, false) // per-schema files use plain, readable CREATE TRIGGER
	return map[string]any{
		"Header": provenance.Render("--", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Source:        strings.Join(s.SourceProtos(), ", "),
			Database:      db.Name,
			Schema:        s.Name,
		}),
		"SchemaQ":   quoteIdent(s.Name),
		"Enums":     enums,
		"Tables":    tables,
		"Functions": ddl.Functions,
		"Triggers":  ddl.Triggers,
		"Comments":  ddl.Comments,
	}
}

// enumDDLViews builds the CREATE TYPE view list for a schema's enums.
func enumDDLViews(s *schema.Schema) []enumDDLView {
	var enums []enumDDLView
	for _, e := range s.Enums {
		vals := make([]string, len(e.Values))
		for i, v := range e.Values {
			vals[i] = quoteLiteral(v.MapName)
		}
		comment := e.Comment
		if comment == "" {
			comment = e.LocalName + " enum values."
		}
		enums = append(enums, enumDDLView{
			Comment:   comment,
			TypeRef:   qualified(s.Name, e.LocalSQLName),
			ValueList: strings.Join(vals, ", "),
		})
	}
	return enums
}

// schemaDDLOf builds the trigger functions, triggers, and COMMENT ON statements
// for one schema. A function is emitted once per distinct auto-update column
// name (set_updated_at, set_update_time, …) and qualified to the schema so it
// never collides across schemas; each owning table gets a BEFORE UPDATE trigger
// that fires it. COMMENT ON persists every table/column comment into the catalog
// (the inline -- comments in the DDL file are not stored by PostgreSQL).
//
// idempotent emits a DROP TRIGGER IF EXISTS before each CREATE TRIGGER so the
// consolidated migrate.sql is safe to re-apply on every supported PostgreSQL
// version; the per-schema reference files pass false for a plain, readable
// CREATE TRIGGER.
func schemaDDLOf(s *schema.Schema, idempotent bool) schemaDDL {
	var ddl schemaDDL
	funcSeen := map[string]bool{}
	for _, t := range s.Tables {
		for _, c := range t.Columns {
			if c.AutoUpdate {
				if !funcSeen[c.Name] {
					funcSeen[c.Name] = true
					ddl.Functions = append(ddl.Functions, updatedAtFunction(s.Name, c.Name))
				}
				ddl.Triggers = append(ddl.Triggers, updatedAtTrigger(s.Name, t.Name, c.Name, idempotent))
			}
		}
		ddl.Comments = append(ddl.Comments, tableComments(s.Name, t)...)
	}
	return ddl
}

// updatedAtFunction renders the trigger function that stamps now() onto an
// auto-update column on every UPDATE. CREATE OR REPLACE makes it re-runnable.
func updatedAtFunction(schemaName, col string) string {
	return fmt.Sprintf(
		"CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS $$\nBEGIN\n    NEW.%s = now();\n    RETURN NEW;\nEND;\n$$ LANGUAGE plpgsql;",
		qualified(schemaName, "set_"+col), quoteIdent(col))
}

// updatedAtTrigger renders the BEFORE UPDATE trigger wiring a table to its
// auto-update function. When idempotent, a DROP TRIGGER IF EXISTS precedes the
// CREATE so the statement re-applies cleanly — and unlike CREATE OR REPLACE
// TRIGGER (PostgreSQL 14+), this works on every supported PostgreSQL version.
func updatedAtTrigger(schemaName, table, col string, idempotent bool) string {
	name := quoteIdent("trg_" + table + "_" + col)
	tbl := qualified(schemaName, table)
	create := fmt.Sprintf(
		"CREATE TRIGGER %s BEFORE UPDATE ON %s\n    FOR EACH ROW EXECUTE FUNCTION %s();",
		name, tbl, qualified(schemaName, "set_"+col))
	if idempotent {
		return fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s;\n%s", name, tbl, create)
	}
	return create
}

// tableComments renders COMMENT ON statements for a table and its commented columns.
func tableComments(schemaName string, t *schema.Table) []string {
	var out []string
	ref := qualified(schemaName, t.Name)
	if t.Comment != "" {
		out = append(out, "COMMENT ON TABLE "+ref+" IS "+quoteLiteral(t.Comment)+";")
	}
	for _, c := range t.Columns {
		if c.Comment != "" {
			out = append(out, "COMMENT ON COLUMN "+ref+"."+quoteIdent(c.Name)+" IS "+quoteLiteral(c.Comment)+";")
		}
	}
	return out
}

// migrateTableView is one table in the consolidated migrate.sql: columns only,
// with foreign keys deferred to ALTER statements (migrateView.Alters) so table
// creation order never matters, even across schemas or reference cycles.
type migrateTableView struct {
	Comment, Ref string
	Cols         []itemView
}

// migrateView assembles the single-file migration covering every schema in db.
// Statements are grouped so the file applies in one shot regardless of
// dependency order: all schemas, then enums, then tables (no inline FKs), then
// FK constraints as ALTER TABLE, then indexes, trigger functions, triggers, and
// COMMENT ON. The whole file is wrapped in a transaction by the template.
func migrateView(db *schema.Database, typeOf types.TypeOf) map[string]any {
	var schemaNames []string
	var enumStmts []string
	var tables []migrateTableView
	var alters, indexes, functions, triggers, comments []string

	for _, s := range db.Schemas {
		schemaNames = append(schemaNames, quoteIdent(s.Name))
		for _, e := range enumDDLViews(s) {
			enumStmts = append(enumStmts, idempotentEnum(e))
		}

		for _, t := range s.Tables {
			ref := qualified(s.Name, t.Name)
			mt := migrateTableView{Comment: t.Comment, Ref: ref}
			for _, col := range t.Columns {
				mt.Cols = append(mt.Cols, itemView{Comment: col.Comment, Def: colDef(s, col, typeOf)})
			}
			if n := len(mt.Cols); n > 0 {
				mt.Cols[n-1].Last = true
			}
			tables = append(tables, mt)

			// Drop-then-add makes each FK re-runnable: the first apply is a no-op
			// drop + add, a re-apply replaces the existing constraint in place.
			for _, fk := range t.ForeignKeys {
				cname := quoteIdent("fk_" + t.Name + "_" + fk.Column)
				alters = append(alters,
					"ALTER TABLE "+ref+" DROP CONSTRAINT IF EXISTS "+cname+";",
					"ALTER TABLE "+ref+" ADD "+fkDef(t.Name, fk)+";")
			}
			indexes = append(indexes, indexStmts(s, t, true)...)
		}

		ddl := schemaDDLOf(s, true) // DROP + CREATE TRIGGER — re-runnable on every PostgreSQL version
		functions = append(functions, ddl.Functions...)
		triggers = append(triggers, ddl.Triggers...)
		comments = append(comments, ddl.Comments...)
	}

	return map[string]any{
		"Header": provenance.Render("--", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Database:      db.Name,
			SchemaLabel:   "schemas",
			Schema:        strings.Join(schemaLabels(db), ", "),
			Notes:         []string{"Single-file migration: every schema in one transaction. Idempotent — safe to re-apply."},
		}),
		"Schemas":   schemaNames,
		"EnumStmts": enumStmts,
		"Tables":    tables,
		"Alters":    alters,
		"Indexes":   indexes,
		"Functions": functions,
		"Triggers":  triggers,
		"Comments":  comments,
	}
}

// idempotentEnum wraps CREATE TYPE in a DO block that swallows the duplicate
// error, since PostgreSQL has no CREATE TYPE IF NOT EXISTS. This lets the
// consolidated migration re-run without failing on an enum that already exists.
// The leading -- comment carries the enum's documentation, matching the
// per-schema files.
func idempotentEnum(e enumDDLView) string {
	return fmt.Sprintf(
		"-- %s\nDO $$ BEGIN\n    CREATE TYPE %s AS ENUM (%s);\nEXCEPTION WHEN duplicate_object THEN null;\nEND $$;",
		e.Comment, e.TypeRef, e.ValueList)
}

// schemaLabels returns the bare schema names for the migrate.sql banner.
func schemaLabels(db *schema.Database) []string {
	out := make([]string, 0, len(db.Schemas))
	for _, s := range db.Schemas {
		out = append(out, s.Name)
	}
	return out
}

// tableViewOf renders one table's column defs, FK constraints, and indexes.
func tableViewOf(s *schema.Schema, t *schema.Table, typeOf types.TypeOf) tableView {
	tv := tableView{Comment: t.Comment, Ref: qualified(s.Name, t.Name)}

	for _, col := range t.Columns {
		tv.Items = append(tv.Items, itemView{Comment: col.Comment, Def: colDef(s, col, typeOf)})
	}
	for _, fk := range t.ForeignKeys {
		tv.Items = append(tv.Items, itemView{Def: fkDef(t.Name, fk)})
	}
	if n := len(tv.Items); n > 0 {
		tv.Items[n-1].Last = true
	}

	tv.Indexes = indexStmts(s, t, false)
	return tv
}

// indexStmts renders the CREATE INDEX statements for a table. Names come from the
// build's nameIndexes pass, so the consolidated migrate.sql and per-schema files
// reference identical index identifiers. ifNotExists adds IF NOT EXISTS so the
// re-runnable migrate.sql skips indexes that already exist.
func indexStmts(s *schema.Schema, t *schema.Table, ifNotExists bool) []string {
	var out []string
	guard := ""
	if ifNotExists {
		guard = "IF NOT EXISTS "
	}
	for _, idx := range t.Indexes {
		cols := make([]string, len(idx.Columns))
		for i, c := range idx.Columns {
			cols[i] = quoteIdent(c)
		}
		unique := ""
		if idx.Unique {
			unique = "UNIQUE "
		}
		out = append(out, fmt.Sprintf(
			"CREATE %sINDEX %s%s ON %s (%s);",
			unique, guard, quoteIdent(idx.Name), qualified(s.Name, t.Name), strings.Join(cols, ", "),
		))
	}
	return out
}

// colDef renders a single column definition fragment.
// Enum columns use the schema-qualified type created by CREATE TYPE.
func colDef(s *schema.Schema, col *schema.Column, typeOf types.TypeOf) string {
	sqlType := typeOf(col)
	if col.Enum != nil {
		sqlType = qualified(s.Name, col.Enum.LocalSQLName)
	}
	def := quoteIdent(col.Name) + "  " + sqlType
	if col.NotNull {
		def += "  NOT NULL"
	}
	if col.PrimaryKey {
		def += "  PRIMARY KEY"
	}
	if col.Unique && !col.PrimaryKey {
		def += "  UNIQUE"
	}
	switch {
	case col.Generated == "uuid":
		def += "  DEFAULT gen_random_uuid()"
	case col.Generated == "ulid":
		// PostgreSQL has no native ulid(); the value is generated client-side
		// (Prisma @default(ulid()) or application code).
	case col.Enum != nil && col.Default != "":
		// An enum default is a label, not an expression: quote it as a literal.
		def += "  DEFAULT " + quoteLiteral(col.Default)
	case col.Default != "":
		def += "  DEFAULT " + col.Default
	}
	return def
}

// fkDef renders a FOREIGN KEY constraint using the schema-qualified referenced
// table and the actual PK column resolved by the IR build pass.
func fkDef(tableName string, fk *schema.ForeignKey) string {
	refTable := quoteIdent(fk.ReferencedTable)
	if fk.ReferencedSchema != "" {
		refTable = qualified(fk.ReferencedSchema, fk.ReferencedTable)
	}
	def := fmt.Sprintf(
		"CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s(%s)",
		quoteIdent("fk_"+tableName+"_"+fk.Column), quoteIdent(fk.Column),
		refTable, quoteIdent(fk.ReferencedColumn),
	)
	if fk.OnDelete != "" {
		def += " ON DELETE " + fk.OnDelete
	}
	if fk.OnUpdate != "" {
		def += " ON UPDATE " + fk.OnUpdate
	}
	return def
}
