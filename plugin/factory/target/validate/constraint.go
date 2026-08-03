package validate

// constraint.go projects the parameterized (orm.v1.constraint) rules, the
// companion to the parameterless presets in validate.go. Both produce the same
// three renderings and flow through the same call sites, so a column may carry
// either or both and the output is one coherent set of checks.

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/the-protobuf-project/orm/plugin/pb/ormpbv1"
	"github.com/the-protobuf-project/protokit/schema"
	"google.golang.org/protobuf/proto"
)

// Constraint is one resolved parameterized rule. Call and SQL both use "{}" for
// the value reference — the Go accessor and the column reference respectively —
// so the caller substitutes without knowing which rule it holds.
type Constraint struct {
	// Call is a Go boolean expression, true when the value is ACCEPTABLE,
	// e.g. "validatex.GTE({}, 1)".
	Call string

	// SQL is the Postgres CHECK body, or "" when the rule has no faithful SQL
	// form.
	SQL string

	// Tag is the go-playground/validator fragment, or "" when it has no builtin.
	Tag string

	// Message completes "<field> must ...".
	Message string
}

// Constraints returns the column's parameterized rules in a fixed order —
// bounds, then pattern, then membership — so output is stable regardless of the
// order the fields were written in.
func Constraints(col *schema.Column) []Constraint {
	o := constraintOpts(col)
	if o == nil {
		return nil
	}
	var out []Constraint

	// min/max read according to what the column is: the value itself when it is
	// a number, the character count when it is a string, the element count when
	// it is a list. The author already knows which, so one pair of fields serves
	// all three rather than making them pick the right spelling.
	b := boundsFor(col)
	if o.Min != nil {
		lit := num(o.GetMin())
		out = append(out, Constraint{
			Call:    fmt.Sprintf("validatex.%s({}, %s)", b.minFn, lit),
			SQL:     fmt.Sprintf("%s >= %s", b.sqlRef, lit),
			Tag:     "min=" + lit,
			Message: fmt.Sprintf(b.message, "at least", lit),
		})
	}
	if o.Max != nil {
		lit := num(o.GetMax())
		out = append(out, Constraint{
			Call:    fmt.Sprintf("validatex.%s({}, %s)", b.maxFn, lit),
			SQL:     fmt.Sprintf("%s <= %s", b.sqlRef, lit),
			Tag:     "max=" + lit,
			Message: fmt.Sprintf(b.message, "at most", lit),
		})
	}

	if p := o.GetPattern(); p != "" {
		out = append(out, Constraint{
			// The pattern reaches Go as a raw string literal, so it needs no
			// escaping there; the SQL literal doubles any quote.
			Call:    fmt.Sprintf("validatex.Pattern({}, `%s`)", p),
			SQL:     fmt.Sprintf("{} ~ %s", sqlLiteral(p)),
			Message: "match " + p,
		})
	}

	if vals := o.GetIn(); len(vals) > 0 {
		out = append(out, Constraint{
			Call:    fmt.Sprintf("validatex.OneOf({}, %s)", goList(vals)),
			SQL:     fmt.Sprintf("{} IN (%s)", sqlList(vals)),
			Tag:     "oneof=" + strings.Join(vals, " "),
			Message: "be one of " + strings.Join(vals, ", "),
		})
	}
	if vals := o.GetNotIn(); len(vals) > 0 {
		out = append(out, Constraint{
			Call:    fmt.Sprintf("validatex.NoneOf({}, %s)", goList(vals)),
			SQL:     fmt.Sprintf("{} NOT IN (%s)", sqlList(vals)),
			Message: "not be one of " + strings.Join(vals, ", "),
		})
	}
	return out
}

// boundKind is how min/max render for one column: which predicate, which SQL
// reference, and how the violation reads.
type boundKind struct {
	minFn, maxFn string
	sqlRef       string // "{}" for a value bound, "length({})" / "cardinality({})" for a size
	message      string // two verbs: the comparison, then the bound
}

// boundsFor picks the reading of min/max from the column's cardinality and type.
func boundsFor(col *schema.Column) boundKind {
	switch {
	case col.List:
		return boundKind{"MinItems", "MaxItems", "cardinality({})", "have %s %s elements"}
	case slices.Contains(numeric, col.Type):
		return boundKind{"GTE", "LTE", "{}", "be %s %s"}
	default:
		return boundKind{"MinLen", "MaxLen", "length({})", "have %s %s characters"}
	}
}

// num formats a bound without a trailing ".000000", so an integer bound reads as
// an integer in the generated code and the DDL.
func num(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// goList renders values as a Go argument list.
func goList(vals []string) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = strconv.Quote(v)
	}
	return strings.Join(out, ", ")
}

// sqlList renders values as a SQL IN list.
func sqlList(vals []string) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = sqlLiteral(v)
	}
	return strings.Join(out, ", ")
}

// sqlLiteral single-quotes a value, doubling any embedded quote.
func sqlLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// VerifyConstraints reports the ways a column's parameterized rules contradict
// its type or each other. As with the presets these are hard errors: a pattern
// that does not compile, or a bound no value can satisfy, would otherwise be
// dropped silently or break the user's build instead of this one.
func VerifyConstraints(col *schema.Column) []string {
	o := constraintOpts(col)
	if o == nil {
		return nil
	}
	var out []string

	numericCol := slices.Contains(numeric, col.Type) && !col.List
	textCol := slices.Contains(text, col.Type) && !col.List

	// min/max apply to every column kind — value, length, or element count — so
	// the only unusable case is a column that is none of those.
	if (o.Min != nil || o.Max != nil) && !numericCol && !textCol && !col.List {
		out = append(out, "min/max apply to numeric, string, or repeated columns")
	}
	if o.GetPattern() != "" && !textCol {
		out = append(out, "pattern applies to string columns")
	}
	if (len(o.GetIn()) > 0 || len(o.GetNotIn()) > 0) && !textCol {
		out = append(out, "in/not_in apply to string columns")
	}

	if p := o.GetPattern(); p != "" {
		if _, err := regexp.Compile(p); err != nil {
			out = append(out, fmt.Sprintf("pattern %q does not compile: %v", p, err))
		}
		// The pattern is embedded in a Go struct tag inside a raw string
		// literal, which a backtick would terminate.
		if strings.Contains(p, "`") {
			out = append(out, "pattern may not contain a backtick")
		}
	}

	// A fractional bound would emit a Go call whose untyped constant cannot
	// convert — a compile error in the user's generated tree, which is exactly
	// the failure this build should absorb instead. Lengths and element counts
	// are whole by nature; an integer column is whole by declaration.
	whole := col.List || textCol || slices.Contains(integral, col.Type)
	if whole {
		for name, v := range map[string]*float64{"min": o.Min, "max": o.Max} {
			if v != nil && strings.ContainsAny(num(*v), ".eE") {
				kind := "an integer column"
				if col.List || textCol {
					kind = "a length"
				}
				out = append(out, fmt.Sprintf("%s bound %s is fractional but %s is whole", name, num(*v), kind))
			}
		}
	}
	if o.Min != nil && o.Max != nil && o.GetMin() > o.GetMax() {
		out = append(out, fmt.Sprintf("min %s is greater than max %s, so no value can satisfy both", num(o.GetMin()), num(o.GetMax())))
	}
	// A max wider than the column's own sizing can never be reached: the column
	// type rejects the value first, with a driver error rather than a
	// field-shaped one.
	if o.Max != nil && textCol {
		if sized := columnMaxLength(col); sized > 0 && o.GetMax() > float64(sized) {
			out = append(out, fmt.Sprintf("max %s exceeds the column's max_length %d, so it can never be reached", num(o.GetMax()), sized))
		}
	}
	return out
}

// constraintOpts returns the column's constraint annotation, nil when absent or
// when the column was synthesized by protokit.
func constraintOpts(col *schema.Column) *ormpbv1.ConstraintOptions {
	if col == nil || col.Source == nil || !proto.HasExtension(col.Source.Options(), ormpbv1.E_Constraint) {
		return nil
	}
	return proto.GetExtension(col.Source.Options(), ormpbv1.E_Constraint).(*ormpbv1.ConstraintOptions)
}

// columnMaxLength reads orm.v1.column's max_length sizing off the column, 0 when
// unset — the one place the two annotations have to agree.
func columnMaxLength(col *schema.Column) int32 {
	if col == nil || col.Source == nil || !proto.HasExtension(col.Source.Options(), ormpbv1.E_Column) {
		return 0
	}
	return proto.GetExtension(col.Source.Options(), ormpbv1.E_Column).(*ormpbv1.ColumnOptions).GetMaxLength()
}
