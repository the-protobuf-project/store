package gorm

// validate_view.go builds the gorm target's validation views: the shared
// validatex runtime package, and (see checksFor) the per-column predicate calls
// a model's Validate method runs. The preset → predicate mapping itself lives in
// the shared target/validate table so the SQL target's CHECK constraints and
// these calls can never disagree.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/the-protobuf-project/orm/plugin/factory/target/validate"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/schema"
)

// checkView is one predicate call in a model's generated Validate method. Cond
// is a Go expression that evaluates to true when the value is ACCEPTABLE, so the
// template negates it once and the nil-guard for an optional column composes
// into the same expression.
type checkView struct {
	// Field is the database column name, so a violation reads the same as the
	// error the equivalent CHECK constraint would raise.
	Field string

	// Cond is the Go boolean expression, e.g.
	//	validatex.Email(m.Email)
	//	m.Website == nil || validatex.URL(*m.Website)
	Cond string

	// Message completes "<field> must ...", or stands alone when Row is set.
	Message string

	// Row marks a cross-field check, whose message describes the row rather than
	// one field — so the generated text reads "<message>" instead of
	// "<field> must <message>".
	Row bool
}

// modelChecks builds the checks for one table, in column then annotation order.
// A preset that does not apply to the column's neutral type or cardinality is
// skipped here; reporting it as a generation-time error is the strict-mode pass.
func modelChecks(t *schema.Table) []checkView {
	var out []checkView
	for _, col := range t.Columns {
		for _, r := range validate.Rules(col) {
			if !r.Accepts(col) {
				continue
			}
			out = append(out, checkView{
				Field:   col.Name,
				Cond:    subst(col, "validatex."+r.Func+"({})"),
				Message: r.Message,
			})
		}
		// The parameterized (orm.v1.constraint) rules render the same way: each
		// already carries a call with {} standing in for the value.
		for _, c := range validate.Constraints(col) {
			out = append(out, checkView{
				Field:   col.Name,
				Cond:    subst(col, c.Call),
				Message: c.Message,
			})
		}
	}
	// Cross-field constraints run after the per-column ones, so a row is reported
	// as failing its columns before its shape. Only those declaring a CEL twin
	// are enforced application-side; the rest are the database's alone.
	for _, tc := range validate.TableChecks(t) {
		if tc.CEL == "" {
			continue
		}
		expr, ok := celToGo(tc.CEL, t)
		if !ok {
			continue
		}
		out = append(out, checkView{Field: tc.Name, Cond: expr, Message: tc.Message, Row: true})
	}
	return out
}

// celToGo rewrites the CEL twin into a Go expression over the model receiver.
// It handles the shape these constraints actually take — `this.<field>` compared
// and combined with the usual operators — by rewriting `this.<column>` to the
// model field and leaving the rest alone, since CEL and Go share the syntax for
// comparison and boolean operators.
//
// It reports false for an expression it cannot rewrite faithfully rather than
// emitting a guess: a check that silently differs from the constraint it mirrors
// is worse than no check, and the database still enforces the constraint.
func celToGo(cel string, t *schema.Table) (string, bool) {
	out, ok := celStringsToGo(cel)
	if !ok {
		return "", false
	}
	replaced := false
	for _, col := range t.Columns {
		ref := "this." + col.Name
		if !strings.Contains(out, ref) {
			continue
		}
		acc := "m." + gormFieldName(col)
		// A nullable column is a pointer in Go; comparing it directly would not
		// compile, and dereferencing it unguarded would panic.
		if strings.HasPrefix(goType(col), "*") {
			return "", false
		}
		out = strings.ReplaceAll(out, ref, acc)
		replaced = true
	}
	// Anything still naming `this.` refers to a column this table does not have.
	if !replaced || strings.Contains(out, "this.") {
		return "", false
	}
	return out, true
}

// celStringsToGo rewrites CEL string literals into Go ones. CEL accepts either
// quote style; Go reserves single quotes for runes, so a CEL `'free'` compiled as
// Go is an illegal rune literal — which is how this was caught. Double-quoted
// literals pass through untouched.
//
// Reports false for an unterminated literal rather than emitting something that
// happens to parse.
func celStringsToGo(cel string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(cel); {
		c := cel[i]
		if c != '\'' && c != '"' {
			b.WriteByte(c)
			i++
			continue
		}
		quote := c
		i++
		var lit strings.Builder
		closed := false
		for i < len(cel) {
			if cel[i] == '\\' && i+1 < len(cel) {
				lit.WriteByte(cel[i])
				lit.WriteByte(cel[i+1])
				i += 2
				continue
			}
			if cel[i] == quote {
				i++
				closed = true
				break
			}
			lit.WriteByte(cel[i])
			i++
		}
		if !closed {
			return "", false
		}
		// Re-quote through strconv so embedded quotes and escapes come out valid
		// Go regardless of which style the author used.
		unquoted, err := strconv.Unquote(`"` + strings.ReplaceAll(lit.String(), `"`, `\"`) + `"`)
		if err != nil {
			return "", false
		}
		b.WriteString(strconv.Quote(unquoted))
	}
	return b.String(), true
}

// validateTag is the go-playground fragment for a column's presets, empty when
// the validation opt is off. Gated by the opt for the same reason the Validate
// method is: one switch turns the whole surface on, matching how the stores,
// filters, and telemetry opts behave.
func validateTag(db *schema.Database, col *schema.Column) string {
	if !dbValidation(db) {
		return ""
	}
	return validate.Tag(col)
}

// subst renders one check against the model receiver, substituting the column's
// accessor into the call's "{}" placeholder. A column whose Go field is a pointer
// is skipped when absent, and the guard composes into the same expression:
// presence is google.api.field_behavior's concern (it already drove NOT NULL), so
// a rule only judges a value that exists.
func subst(col *schema.Column, call string) string {
	acc := "m." + gormFieldName(col)
	if strings.HasPrefix(goType(col), "*") {
		return acc + " == nil || " + strings.ReplaceAll(call, "{}", "*"+acc)
	}
	return strings.ReplaceAll(call, "{}", acc)
}

// validatexView is the view for the shared, stdlib-only validation runtime.
// The package is static apart from its header, like filterx.
func validatexView(db *schema.Database) map[string]any {
	return map[string]any{
		"Header": header.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Database:      db.Name,
			SchemaLabel:   "package",
			Schema:        validatexPkg,
			Notes:         []string{"Shared validation predicates driven by the generated models' Validate methods."},
		}),
		"Package": validatexPkg,
	}
}

// validatexImport is the Go import path of the shared validation runtime.
func validatexImport(db *schema.Database) string {
	return dbGoModule(db) + "/" + validatexPkg
}

// assignTableChecks maps each cross-field constraint onto a distinct column
// whose tag will carry it. GORM turns a field's check: tag into a table-level
// constraint, so which column carries it does not affect the result — but a
// field's tag is a map keyed by setting name, so two constraints on one field
// would leave only the last. One column each is what keeps them all.
//
// Columns already carrying their own column-level check are skipped for the same
// reason. Returns an empty map when the table declares no cross-field
// constraints.
func assignTableChecks(db *schema.Database, t *schema.Table) map[string]string {
	checks := validate.TableChecks(t)
	if len(checks) == 0 || !dbValidationDB(db) {
		return nil
	}
	var free []*schema.Column
	for _, col := range t.Columns {
		if _, taken := validate.ColumnCheck(t.Name, col, col.Name); taken {
			continue
		}
		if col.Enum != nil {
			continue // its tag already carries the enum value check
		}
		free = append(free, col)
	}
	out := make(map[string]string, len(checks))
	for i, c := range checks {
		if i >= len(free) {
			break // reported as an error by verifyTableCheckCapacity
		}
		out[free[i].Name] = "check:" + c.ConstraintName + "," + validate.TagValue(c.SQL)
	}
	return out
}

// TableCheckCapacity reports whether the table has enough columns free of their
// own check constraint to carry each cross-field constraint on a distinct one.
// Returning an error is the right outcome: silently dropping a constraint the
// sql target emits would leave the two backends enforcing different rules.
func TableCheckCapacity(t *schema.Table) []string {
	checks := validate.TableChecks(t)
	if len(checks) == 0 {
		return nil
	}
	free := 0
	for _, col := range t.Columns {
		if _, taken := validate.ColumnCheck(t.Name, col, col.Name); taken || col.Enum != nil {
			continue
		}
		free++
	}
	if len(checks) > free {
		return []string{fmt.Sprintf(
			"%d table constraints need %d columns without their own check to carry them, but only %d are free; "+
				"GORM keeps one check per field, so the rest would be dropped from AutoMigrate while the sql target still emits them",
			len(checks), len(checks), free)}
	}
	return nil
}
