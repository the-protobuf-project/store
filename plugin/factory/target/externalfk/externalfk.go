// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package externalfk renders the constraint-only foreign keys protokit records
// in Table.ExternalForeignKeys: references whose target lives in another
// database of the same physical Postgres (another database of this run, or one
// discovered from a reference image), or in this database but resolved only by
// resource type. Each becomes a plain FOREIGN KEY constraint and nothing else:
// no association, no relation field, no GraphQL relationship.
//
// The constraint is added after every table of every database exists, NOT
// VALID, and only when it is missing and its target table is there, so the
// statement is safe to run on every start, before or after the other
// database's migration, and on a table that already holds rows a constraint
// would reject. VALIDATE CONSTRAINT (Validate) checks the existing rows later,
// once they are clean; new writes are checked from the moment it is added.
//
// The SQL and GORM targets share these statements so the two migrations agree
// on every name.
package externalfk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/schema"
)

// maxIdent is PostgreSQL's identifier limit (NAMEDATALEN - 1).
const maxIdent = 63

// Constraint is one rendered external foreign key.
type Constraint struct {
	Name   string // constraint name, at most 63 bytes
	Table  string // quoted, schema-qualified referencing table
	Target string // quoted, schema-qualified referenced table
	Column string // referencing column
	Ref    string // referenced column
	// Kind is how protokit resolved it (cross-database, discovered, local).
	Kind string
	// Database is the database the target belongs to.
	Database string

	onDel, onUpd string
}

// Name is the constraint name of fk on table: "fk_<table>_<column>", as every
// other foreign key the targets emit, shortened deterministically when it
// would pass Postgres's 63-byte limit (which would otherwise truncate it
// silently, so an existence check by the long name would never match).
func Name(table string, fk *schema.ForeignKey) string {
	n := "fk_" + table + "_" + fk.Column
	if len(n) <= maxIdent {
		return n
	}
	sum := sha256.Sum256([]byte(n))
	return n[:maxIdent-9] + "_" + hex.EncodeToString(sum[:])[:8]
}

// For lists every external foreign key of db's tables, in schema and table
// order.
func For(db *schema.Database) []Constraint {
	var out []Constraint
	for _, s := range db.Schemas {
		for _, t := range s.Tables {
			for _, fk := range t.ExternalForeignKeys {
				out = append(out, Constraint{
					Name:     Name(t.Name, fk),
					Table:    qualified(s.Name, t.Name),
					Target:   qualified(fk.ReferencedSchema, fk.ReferencedTable),
					Column:   fk.Column,
					Ref:      fk.ReferencedColumn,
					Kind:     fk.Kind.String(),
					onDel:    fk.OnDelete,
					onUpd:    fk.OnUpdate,
					Database: fk.ReferencedDatabase,
				})
			}
		}
	}
	return out
}

// Add is the idempotent statement adding c: a DO block that adds the
// constraint NOT VALID when it does not exist yet and its target table does,
// and otherwise only says why it skipped.
func (c Constraint) Add() string {
	action := c.onDel
	if action == "" {
		action = "NO ACTION"
	}
	def := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s(%s) ON DELETE %s",
		c.Table, quoteIdent(c.Name), quoteIdent(c.Column), c.Target, quoteIdent(c.Ref), action)
	if c.onUpd != "" {
		def += " ON UPDATE " + c.onUpd
	}
	return fmt.Sprintf(`-- %s.%s -> %s (%s, database %s)
DO $$
BEGIN
    IF to_regclass(%s) IS NULL THEN
        RAISE NOTICE 'skipping %s: %s does not exist yet';
    ELSIF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = %s AND conrelid = %s::regclass) THEN
        %s NOT VALID;
    END IF;
END $$;`,
		c.Table, c.Column, c.Target, c.Kind, c.Database,
		literal(c.Target), c.Name, strings.ReplaceAll(c.Target, `'`, `''`),
		literal(c.Name), literal(c.Table), def)
}

// Validate is the statement checking the rows that predate c, once its
// constraint exists and is still NOT VALID. It is a no-op otherwise.
func (c Constraint) Validate() string {
	return fmt.Sprintf(`DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = %s AND conrelid = to_regclass(%s) AND NOT convalidated) THEN
        ALTER TABLE %s VALIDATE CONSTRAINT %s;
    END IF;
END $$;`, literal(c.Name), literal(c.Table), c.Table, quoteIdent(c.Name))
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func qualified(schemaName, name string) string {
	if schemaName == "" {
		return quoteIdent(name)
	}
	return quoteIdent(schemaName) + "." + quoteIdent(name)
}

func literal(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
