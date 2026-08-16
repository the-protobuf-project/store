// Package sql generates PostgreSQL DDL from the store IR.
//
// Output layout — one file per schema, mirroring the prisma fragment tree:
//
//	<db>/<schema>.postgres.sql
//
// Each file carries CREATE SCHEMA, CREATE TYPE for every enum, CREATE TABLE
// with inline comments, FK constraints referencing the resolved PK column,
// and CREATE INDEX statements.
package sql

import (
	"fmt"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/docs"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/protokit/templates"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
	"github.com/the-protobuf-project/store/plugin/factory/target/types"
)

// Generator implements schema.IRTarget for PostgreSQL DDL output.
type Generator struct{}

var _ schema.IRTarget = (*Generator)(nil)

// Name returns the target identifier used in buf.gen.yaml opt: [target=sql].
func (g *Generator) Name() string { return "sql" }

// Generate renders from the databases alone, for callers that have no IR. Column
// types then fall back to the neutral FieldType, since the store.v1 overrides live
// in the facets this form does not carry.
func (g *Generator) Generate(p *protogen.Plugin, dbs []*schema.Database) error {
	return g.GenerateIR(p, &schema.IR{Databases: dbs})
}

// GenerateIR writes one .postgres.sql file per schema plus a consolidated
// migrate.sql into the plugin response.
func (g *Generator) GenerateIR(p *protogen.Plugin, ir *protokit.IR) error {
	fx := facets.New(ir)
	typeOf := func(c *schema.Column) string { return types.SQLForColumn(c, fx.Column(c)) }
	for _, db := range ir.Databases {
		if types.Provider(db.Provider) != types.Postgres {
			return fmt.Errorf("sql: database %q uses provider %q — the sql target only supports postgres", db.Name, db.Provider)
		}
		for _, s := range db.Schemas {
			path := fmt.Sprintf("%s/%s.postgres.sql", db.Name, s.Name)
			f := p.NewGeneratedFile(path, "")
			if err := templates.Render(tmpl, f, "schema.sql.tpl", schemaView(db, s, typeOf, fx)); err != nil {
				return fmt.Errorf("sql: %s: %w", path, err)
			}
		}
		// Consolidated single-file migration: every schema, ordered so it applies
		// in one transaction (foreign keys deferred to ALTER statements).
		migratePath := db.Name + "/migrate.sql"
		mf := p.NewGeneratedFile(migratePath, "")
		if err := templates.Render(tmpl, mf, "migrate.sql.tpl", migrateView(db, typeOf, fx)); err != nil {
			return fmt.Errorf("sql: %s: %w", migratePath, err)
		}
		rf := p.NewGeneratedFile(db.Name+"/README.md", "")
		md := docs.Render(db, docs.Meta{
			Title:   "PostgreSQL schema",
			Tagline: "CREATE SCHEMA / TYPE / TABLE DDL with foreign keys and indexes.",
			Outputs: []string{
				"`migrate.sql` — the whole database in one transactional file; apply with `psql -f migrate.sql`.",
				"`<schema>.postgres.sql` — one DDL file per schema (apply referenced tables before referencing ones).",
				"Auto-update triggers keep updated-at columns current; COMMENT ON persists field docs to the catalog.",
			},
			Naming: docs.Local(db),
			TypeOf: typeOf,
		})
		if _, err := rf.Write([]byte(md)); err != nil {
			return fmt.Errorf("sql: %s/README.md: %w", db.Name, err)
		}
	}
	return nil
}
