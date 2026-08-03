package validate

// table.go projects the table-level (orm.v1.table_constraint) annotations: a
// CHECK spanning several columns, plus the optional CEL twin the application
// side evaluates.
//
// Unlike the field-level rules, the two halves are authored separately rather
// than derived from one another. SQL and CEL are not inter-translatable in
// general, and guessing a translation would produce a check that silently
// disagrees with the constraint it claims to mirror.

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/orm/plugin/pb/ormpbv1"
	"github.com/the-protobuf-project/protokit/schema"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TableCheck is one resolved cross-field constraint.
type TableCheck struct {
	// Name is the constraint identifier as declared.
	Name string

	// ConstraintName is the DDL identifier, chk_<table>_<name>.
	ConstraintName string

	// SQL is the CHECK body.
	SQL string

	// CEL is the application-side twin, empty when the author declared none — in
	// which case the constraint is enforced by the database alone.
	CEL string

	// Message is the failure text, defaulted to the name.
	Message string
}

// TableChecks returns the table's declared cross-field constraints, in
// declaration order.
func TableChecks(t *schema.Table) []TableCheck {
	var out []TableCheck
	for _, c := range tableConstraints(t) {
		msg := c.GetMessage()
		if msg == "" {
			msg = "satisfy " + c.GetName()
		}
		out = append(out, TableCheck{
			Name:           c.GetName(),
			ConstraintName: tableConstraintName(t.Name, c.GetName()),
			SQL:            c.GetSql(),
			CEL:            c.GetCel(),
			Message:        msg,
		})
	}
	return out
}

// VerifyTable reports the ways a table's cross-field constraints are unusable.
// As elsewhere, these are hard errors: a constraint with no expression would be
// silently inert, and a duplicate name would have one silently overwrite the
// other in the database.
func VerifyTable(t *schema.Table) []string {
	var out []string
	seen := map[string]bool{}
	cols := make([]string, 0, len(t.Columns))
	for _, c := range t.Columns {
		cols = append(cols, c.Name)
	}
	for _, c := range tableConstraints(t) {
		name := c.GetName()
		if name == "" {
			out = append(out, "table constraint declares no name")
			continue
		}
		if seen[name] {
			out = append(out, fmt.Sprintf("duplicate table constraint %q", name))
		}
		seen[name] = true
		if strings.TrimSpace(c.GetSql()) == "" {
			out = append(out, fmt.Sprintf("table constraint %q declares no sql expression", name))
		}
		// The expression is embedded in a GORM struct tag, where a backslash is
		// an invalid Go escape that voids the entire tag — see the note on the
		// rules table. Backslashes are escaped on the way out, but a backtick
		// cannot be escaped at all.
		if strings.Contains(c.GetSql(), "`") || strings.Contains(c.GetCel(), "`") {
			out = append(out, fmt.Sprintf("table constraint %q may not contain a backtick", name))
		}
		// A constraint naming no column of this table is almost certainly a typo:
		// it would still be valid SQL if it referenced only literals, but it would
		// not be checking this row.
		if sql := c.GetSql(); sql != "" && !mentionsAny(sql, cols) {
			out = append(out, fmt.Sprintf("table constraint %q names no column of this table", name))
		}
	}
	return out
}

// mentionsAny reports whether expr contains any of the given column names as a
// whole word.
func mentionsAny(expr string, cols []string) bool {
	for _, c := range cols {
		if word(expr, c) {
			return true
		}
	}
	return false
}

// word reports whether name occurs in expr delimited by non-identifier
// characters, so "end" does not match inside "end_time".
func word(expr, name string) bool {
	for i := 0; ; {
		j := strings.Index(expr[i:], name)
		if j < 0 {
			return false
		}
		j += i
		before := j == 0 || !ident(expr[j-1])
		after := j+len(name) == len(expr) || !ident(expr[j+len(name)])
		if before && after {
			return true
		}
		i = j + len(name)
	}
}

func ident(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// constraintName builds the chk_<table>_<name> identifier, truncated to
// Postgres' 63-byte limit so both backends name it identically rather than
// letting the server truncate one of them.
func tableConstraintName(table, name string) string {
	out := "chk_" + table + "_" + name
	if len(out) > 63 {
		out = out[:63]
	}
	return out
}

// tableConstraints returns the table's declared constraints, nil when absent or
// when the table was synthesized by protokit.
func tableConstraints(t *schema.Table) []*ormpbv1.TableConstraint {
	if t == nil || t.Source == nil {
		return nil
	}
	return constraintsOf(t.Source)
}

func constraintsOf(d protoreflect.MessageDescriptor) []*ormpbv1.TableConstraint {
	if !proto.HasExtension(d.Options(), ormpbv1.E_TableConstraint) {
		return nil
	}
	out, _ := proto.GetExtension(d.Options(), ormpbv1.E_TableConstraint).([]*ormpbv1.TableConstraint)
	return out
}
