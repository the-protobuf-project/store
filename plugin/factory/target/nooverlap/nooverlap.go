// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package nooverlap plans the exclusion constraints (store.v1.table).no_overlap
// asks for: "no two rows sharing these keys may cover overlapping time".
//
// Like searchindex, the plan cannot live in protokit's schema.Index, which has
// no access method, operator or expression, so it is derived here once from the
// store.v1 facets and each target renders it in its own syntax. The SQL target
// writes the statement into its DDL; the gorm target runs the same statement
// after AutoMigrate, which cannot express an EXCLUDE constraint itself.
package nooverlap

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
)

// Extension supplies the GiST operator classes for plain equality columns
// (text, bigint), which an exclusion constraint needs beside the range's &&.
const Extension = "btree_gist"

// Plan is one exclusion constraint on one table.
type Plan struct {
	Name  string   // constraint name
	Table string   // quoted, schema-qualified table: "allocation"."resource"
	Key   []string // equality column names
	Start string   // range start column
	End   string   // range end column
}

// Check validates every no_overlap rule in db, so a misspelt field fails
// generation instead of silently leaving a table unguarded.
func Check(db *schema.Database, fx facets.Set) error {
	for _, s := range db.Schemas {
		for _, t := range s.Tables {
			if _, err := plans(s, t, fx); err != nil {
				return err
			}
		}
	}
	return nil
}

// For returns the plans of table t in schema s. Call Check first: For skips
// a rule Check would reject.
func For(s *schema.Schema, t *schema.Table, fx facets.Set) []Plan {
	out, _ := plans(s, t, fx)
	return out
}

// Extensions returns the extensions plans need: btree_gist when there is one.
func Extensions(plans []Plan) []string {
	if len(plans) == 0 {
		return nil
	}
	return []string{Extension}
}

func plans(s *schema.Schema, t *schema.Table, fx facets.Set) ([]Plan, error) {
	opts := fx.Table(t)
	if opts == nil {
		return nil, nil
	}
	var out []Plan
	for i, rule := range opts.GetNoOverlap() {
		where := fmt.Sprintf("store: %s: (store.v1.table).no_overlap[%d]", t.ProtoMessage, i)
		p := Plan{Table: quote(s.Name) + "." + quote(t.Name)}
		for _, k := range rule.GetKey() {
			c := column(t, k)
			if c == nil {
				return nil, fmt.Errorf("%s: key %q names no field of the message", where, k)
			}
			p.Key = append(p.Key, c.Name)
		}
		for _, bound := range []struct {
			field string
			into  *string
			what  string
		}{{rule.GetStart(), &p.Start, "start"}, {rule.GetEnd(), &p.End, "end"}} {
			c := column(t, bound.field)
			switch {
			case bound.field == "":
				return nil, fmt.Errorf("%s: %s is required", where, bound.what)
			case c == nil:
				return nil, fmt.Errorf("%s: %s %q names no field of the message", where, bound.what, bound.field)
			case c.Type != schema.TypeTimestamp:
				return nil, fmt.Errorf("%s: %s %q is not a google.protobuf.Timestamp", where, bound.what, bound.field)
			}
			*bound.into = c.Name
		}
		p.Name = rule.GetName()
		if p.Name == "" {
			p.Name = truncate(strings.Join(append([]string{t.Name}, p.Key...), "_") + "_no_overlap")
		}
		out = append(out, p)
	}
	return out, nil
}

// DDL renders p as an idempotent statement: the constraint is added only when
// the table does not already have one by that name, so it can run on every
// migration like AutoMigrate itself.
func (p Plan) DDL() string {
	cols := make([]string, 0, len(p.Key)+1)
	for _, k := range p.Key {
		cols = append(cols, quote(k)+" WITH =")
	}
	cols = append(cols, "tstzrange("+quote(p.Start)+", "+quote(p.End)+", '[)') WITH &&")
	return "DO $$ BEGIN\n" +
		"  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = '" + p.Name + "' AND conrelid = '" + p.Table + "'::regclass) THEN\n" +
		"    ALTER TABLE " + p.Table + " ADD CONSTRAINT " + quote(p.Name) + " EXCLUDE USING gist (" + strings.Join(cols, ", ") + ");\n" +
		"  END IF;\n" +
		"END $$;"
}

// column finds the column a proto field name maps to.
func column(t *schema.Table, field string) *schema.Column {
	for _, c := range t.Columns {
		if c.Source != nil && string(c.Source.Name()) == field {
			return c
		}
	}
	for _, c := range t.Columns {
		if c.Name == field {
			return c
		}
	}
	return nil
}

func quote(ident string) string { return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"` }

// truncate keeps a generated name within PostgreSQL's 63-byte identifier limit.
func truncate(name string) string {
	if len(name) > 63 {
		return name[:63]
	}
	return name
}
