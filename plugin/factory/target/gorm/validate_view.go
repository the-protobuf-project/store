package gorm

// validate_view.go builds the gorm target's validation views: the shared
// validatex runtime package, and (see checksFor) the per-column predicate calls
// a model's Validate method runs. The preset → predicate mapping itself lives in
// the shared target/validate table so the SQL target's CHECK constraints and
// these calls can never disagree.

import (
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

	// Message completes "<field> must ...".
	Message string
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
	return out
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
