{{.Header}}

// Package filterx is the generated filter/order/list SDK: the Condition
// vocabulary an AIP-160 filter parses into, the Spec data model the per-schema
// filters.go files instantiate, the shared semantics (enum normalization,
// date/number validation, ILIKE escaping, sort allowlists, page math), and the
// chainable engines built from a Spec — Gorm[M] for SQL, Hasura[M] for
// GraphQL, and OpenSearch for the query DSL. Engines are code, specs are data:
// one Spec per table drives every backend, so all accept identical filter
// strings by construction.
package filterx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// ErrInvalid marks a caller mistake — an unknown filter/sort field, an
// unsupported operator, or a malformed value. Callers translate it to their
// invalid-argument error (e.g. a gRPC InvalidArgument) with errors.Is.
var ErrInvalid = errors.New("invalid filter or order_by")

// ErrUnsupported marks a filter or order_by that is valid but that the
// backend asked cannot evaluate -- tag containment or ordering by a JSON
// attribute path on Hasura DDN, say. It wraps ErrInvalid, so a caller that
// only checks ErrInvalid still answers invalid-argument; errors.Is with
// ErrUnsupported tells the two apart.
var ErrUnsupported = fmt.Errorf("%w: unsupported on this backend", ErrInvalid)

// Op is the comparison in a single filter term.
type Op int

const (
	// OpEq is the `=` operator (exact match).
	OpEq Op = iota
	// OpNeq is the `!=` operator (negated exact match).
	OpNeq
	// OpHas is the `:` operator (substring / membership match).
	OpHas
	// OpLte is the `<=` operator (ordered kinds only).
	OpLte
	// OpGte is the `>=` operator (ordered kinds only).
	OpGte
	// OpLt is the `<` operator (ordered kinds only). A half-open window's
	// bound needs it: `end_time > t` is not the same set as `end_time >= t`.
	OpLt
	// OpGt is the `>` operator (ordered kinds only).
	OpGt
	// OpIn matches any of Condition.Values. AIP-160 has no list syntax, so it
	// is built in code (ListInput.Where) rather than parsed from a filter.
	OpIn
	// OpNull matches a field that is unset (Value "true") or set ("false"). For
	// a reference to a value object it asks whether the object exists.
	OpNull
	// OpOr matches when any of Condition.Any does: `a OR b` in a filter, or
	// built in code.
	OpOr
	// OpAnd matches when every one of Condition.Any does: a parenthesized
	// group, `(a b)`.
	OpAnd
	// OpNot matches when Condition.Any (ANDed) does not: `NOT a` or `-a`. A
	// row the negated test cannot be decided for (a NULL compared) matches,
	// as a document missing the field does on OpenSearch.
	OpNot
)

// Condition is one parsed term of a filter expression. A condition with an
// empty Field is a bareword free-text term, matched against the spec's Search
// columns, unless it is a composite (OpOr, OpAnd, OpNot) over Any.
// Conditions in a slice are AND-combined.
type Condition struct {
	Field string
	Op    Op
	Value string
	// Values are OpIn's alternatives; every other Op reads Value.
	Values []string
	// Any are the operands of a composite: OpOr's alternatives, OpAnd's
	// conjuncts, OpNot's negated conjunction.
	Any []Condition
	// Hint marks a condition that only narrows: an engine that can evaluate
	// it does, one that cannot leaves it out, so the caller must not rely on
	// it for correctness and filters the result itself. It is how a read
	// pushes a window down where the backend can follow a value-object path
	// and stays correct where it cannot.
	Hint bool
	// Quoted records that Value was a quoted string in the filter text. It
	// only matters for a JSON attribute path, where `floor = 3` compares a
	// number and `floor = "3"` a string.
	Quoted bool
}

// Or is the condition "any of these", e.g. a start given as a date or a
// date-time.
func Or(alternatives ...Condition) Condition {
	return Condition{Op: OpOr, Any: alternatives}
}

// And is the condition "all of these", a parenthesized group.
func And(conds ...Condition) Condition {
	return Condition{Op: OpAnd, Any: conds}
}

// Not is the condition "not c".
func Not(c Condition) Condition {
	return Condition{Op: OpNot, Any: []Condition{c}}
}

// IsComposite reports whether c combines other conditions (OpOr, OpAnd,
// OpNot) rather than testing a field.
func IsComposite(c Condition) bool { return c.Op == OpOr || c.Op == OpAnd || c.Op == OpNot }

// IsNull is the condition "field is unset" (null) or "is set" (!null).
func IsNull(field string, null bool) Condition {
	return Condition{Field: field, Op: OpNull, Value: strconv.FormatBool(null)}
}

// AsHint returns c marked as a hint (see Condition.Hint).
func AsHint(c Condition) Condition {
	c.Hint = true
	return c
}

// In is the condition "field is any of values", for callers batching a read
// across many keys at once.
func In(field string, values ...string) Condition {
	return Condition{Field: field, Op: OpIn, Values: values}
}

// Kind classifies a field's filter semantics; it decides which operators apply
// and how values are validated and normalized.
type Kind int

const (
	// KindText is a string column: = / != exact, `:` case-insensitive contains.
	KindText Kind = iota
	// KindEnum is a stored enum column: = / !=, accepting the bare value name
	// ("ROOM") or the fully-qualified proto form ("UNIT_TYPE_ROOM").
	KindEnum
	// KindDate is a calendar-date column: = / <= / >= against YYYY-MM-DD.
	KindDate
	// KindTimestamp is a timestamptz column: = / <= / >= against RFC 3339 (a
	// bare date is accepted as its midnight-UTC instant).
	KindTimestamp
	// KindInt is an integral column: = / != / <= / >=.
	KindInt
	// KindFloat is a floating-point column: = / != / <= / >=.
	KindFloat
	// KindBool is a boolean column: = / != against true|false.
	KindBool
	// KindRef is a stored resource-reference id: = / != identity match only.
	KindRef
	// KindTags is a text[] column: `:` containment of one tag.
	KindTags
	// KindJSON is a jsonb document of arbitrary attributes. The field itself
	// only supports null checks; a dotted path under it
	// (`attributes.floor`, `attributes.view.sea`) is a filter field of its
	// own: = / != match the value at the path, `:` matches a value in an
	// array at the path (or `:*`, the path being present), and the ordered
	// operators compare it. A path FieldSpec.JSONKeys declares compares as its
	// declared kind; any other path takes the value's own type (a quoted
	// value is a string; an unquoted one a number, true/false or null when it
	// reads as one).
	KindJSON
)

// FieldSpec describes one filterable field: its physical column, its kind, and
// (for enums) the proto value prefix stripped during normalization.
type FieldSpec struct {
	Column     string
	Kind       Kind
	EnumPrefix string
	// Via is set for a field of a value object the row points at -- an
	// event's "start.date_time" -- which is matched through the reference
	// rather than on the row itself.
	Via *Via
	// JSONKeys declares the type of hot paths under a KindJSON field
	// ("floor": KindInt), dot-separated below the field. A declared path is
	// validated and compared as its kind, and on OpenSearch it is queried in
	// its typed mapping rather than the flat_object one.
	JSONKeys map[string]Kind
}

// Via locates a value-object field: the row's reference column (FieldSpec's
// Column) names a key of Table, whose Column holds the value.
type Via struct {
	Table  string // quoted, schema-qualified value-object table
	Key    string // its key column
	Column string // the value column
}

// Spec is one table's filter/order surface. Instances are generated per schema
// (see each <schema>/filters.go); both engines consume the same Spec.
type Spec struct {
	// Table is the quoted, schema-qualified physical table the gorm engine
	// prefixes columns with (unused by the hasura engine).
	Table string
	// Fields maps API filter field names to their specs.
	Fields map[string]FieldSpec
	// Search lists the columns a bareword free-text term matches
	// (case-insensitive contains, OR-combined).
	Search []string
	// Sort maps order_by field names to their sortable columns.
	Sort map[string]SortSpec
	// PK lists the table's primary-key columns. OrderTerms appends them as the
	// final order_by terms so every page has a total order: paging over a
	// partially-ordered result lets PostgreSQL return ties in any order between
	// queries, which silently duplicates and skips rows across pages. A total
	// order is also what makes the keyset cursor land on exactly one row.
	PK []SortSpec
}

// SortSpec describes one sortable column: the physical column and the kind that
// decides how a cursor value for it is written into, and recovered from, a page
// token. A column can be sortable without being filterable, so its kind is
// recorded here rather than being looked up in Fields.
type SortSpec struct {
	Column string
	Kind   Kind
	// NotNull drops the NULL half of this column's keyset comparison. It is not
	// only noise: `(col > ? OR col IS NULL)` is a disjunction PostgreSQL cannot
	// serve as one index range scan, where the bare `col > ?` it reduces to can
	// be. Every query carries the primary-key tiebreaker, so this matters most
	// there.
	NotNull bool
}

// --- filter parsing --------------------------------------------------------------

// Parse parses an AIP-160 filter into AND-combined conditions. It supports:
//
//   - `field = value`, `field != value`, `field : value`
//   - `field < value`, `field <= value`, `field > value`, `field >= value`
//     (ordered fields, e.g. dates)
//   - quoted values ("two words") and barewords (SUMMER25)
//   - a bare term with no operator, treated as a free-text search
//   - terms separated by whitespace or an explicit `AND`
//   - `a OR b`, which binds tighter than AND as AIP-160 says: `a b OR c` is
//     `a AND (b OR c)`
//   - `NOT a` and `-a`
//   - parentheses grouping any of these
//
// AND, OR and NOT are keywords only unquoted; OR and NOT only in capitals
// (AND in any case, as before). It validates only syntax; the engines
// validate each Field against the spec. A malformed expression yields
// ErrInvalid. The empty string parses to no conditions (an unfiltered list).
func Parse(filter string) ([]Condition, error) {
	toks, err := tokenize(filter)
	if err != nil {
		return nil, err
	}
	p := &filterParser{toks: toks}
	conds, err := p.expression(0)
	if err != nil {
		return nil, err
	}
	if p.i < len(toks) {
		return nil, fmt.Errorf("%w: unbalanced %q in filter", ErrInvalid, toks[p.i].text)
	}
	return conds, nil
}

// maxFilterDepth bounds the nesting of parentheses and NOTs, so a hostile
// filter cannot exhaust the stack.
const maxFilterDepth = 32

// filterParser is a recursive-descent parser over AIP-160's grammar:
//
//	expression = sequence { AND sequence }   (whitespace is AND too)
//	factor     = term { OR term }
//	term       = [ NOT | "-" ] term | simple
//	simple     = restriction | "(" expression ")"
type filterParser struct {
	toks []token
	i    int
}

func (p *filterParser) at(kw string) bool {
	if p.i >= len(p.toks) {
		return false
	}
	t := p.toks[p.i]
	if t.op || t.quoted || t.paren != 0 {
		return false
	}
	if kw == "AND" {
		return strings.EqualFold(t.text, kw)
	}
	return t.text == kw
}

func (p *filterParser) atParen(c byte) bool {
	return p.i < len(p.toks) && p.toks[p.i].paren == c
}

// expression parses factors up to the end or a closing parenthesis, ANDed;
// a parenthesized group in it is flattened into it.
func (p *filterParser) expression(depth int) ([]Condition, error) {
	var conds []Condition
	for {
		for p.at("AND") {
			p.i++
		}
		if p.i >= len(p.toks) || p.atParen(')') {
			return conds, nil
		}
		c, err := p.factor(depth)
		if err != nil {
			return nil, err
		}
		if c.Op == OpAnd {
			conds = append(conds, c.Any...)
		} else {
			conds = append(conds, c)
		}
	}
}

func (p *filterParser) factor(depth int) (Condition, error) {
	c, err := p.term(depth)
	if err != nil {
		return Condition{}, err
	}
	alts := []Condition{c}
	for p.at("OR") {
		p.i++
		if p.i >= len(p.toks) || p.atParen(')') || p.at("AND") || p.at("OR") {
			return Condition{}, fmt.Errorf("%w: OR is missing its right operand", ErrInvalid)
		}
		alt, err := p.term(depth)
		if err != nil {
			return Condition{}, err
		}
		alts = append(alts, alt)
	}
	if len(alts) == 1 {
		return c, nil
	}
	return Or(alts...), nil
}

func (p *filterParser) term(depth int) (Condition, error) {
	negate := false
	if p.at("NOT") {
		p.i++
		negate = true
	} else if t := p.toks[p.i]; !t.op && !t.quoted && t.paren == 0 && strings.HasPrefix(t.text, "-") {
		if t.text != "-" {
			p.toks[p.i].text = t.text[1:] // -a
		} else if t.negates {
			p.i++ // -"a b", -(a b)
		} else {
			return Condition{}, fmt.Errorf("%w: %q must prefix a term", ErrInvalid, "-")
		}
		negate = true
	}
	if !negate {
		return p.simple(depth)
	}
	if depth >= maxFilterDepth {
		return Condition{}, fmt.Errorf("%w: filter nests deeper than %d", ErrInvalid, maxFilterDepth)
	}
	if p.i >= len(p.toks) || p.atParen(')') || p.at("AND") || p.at("OR") {
		return Condition{}, fmt.Errorf("%w: NOT is missing its operand", ErrInvalid)
	}
	c, err := p.term(depth + 1)
	if err != nil {
		return Condition{}, err
	}
	return Not(c), nil
}

func (p *filterParser) simple(depth int) (Condition, error) {
	t := p.toks[p.i]
	switch {
	case t.paren == '(':
		if depth >= maxFilterDepth {
			return Condition{}, fmt.Errorf("%w: filter nests deeper than %d", ErrInvalid, maxFilterDepth)
		}
		p.i++
		conds, err := p.expression(depth + 1)
		if err != nil {
			return Condition{}, err
		}
		if !p.atParen(')') {
			return Condition{}, fmt.Errorf("%w: unbalanced %q in filter", ErrInvalid, "(")
		}
		p.i++
		switch len(conds) {
		case 0:
			return Condition{}, fmt.Errorf("%w: empty parentheses in filter", ErrInvalid)
		case 1:
			return conds[0], nil
		}
		return And(conds...), nil
	case t.paren != 0:
		return Condition{}, fmt.Errorf("%w: unbalanced %q in filter", ErrInvalid, t.text)
	case t.op:
		return Condition{}, fmt.Errorf("%w: unexpected operator %q in filter", ErrInvalid, t.text)
	}
	// A value token followed by an operator is a `field op value` term.
	if p.i+1 < len(p.toks) && p.toks[p.i+1].op {
		if p.i+2 >= len(p.toks) || p.toks[p.i+2].op || p.toks[p.i+2].paren != 0 {
			return Condition{}, fmt.Errorf("%w: filter operator %q is missing a value", ErrInvalid, p.toks[p.i+1].text)
		}
		op, err := parseOp(p.toks[p.i+1].text)
		if err != nil {
			return Condition{}, err
		}
		v := p.toks[p.i+2]
		p.i += 3
		return Condition{Field: t.text, Op: op, Value: v.text, Quoted: v.quoted}, nil
	}
	// Otherwise it's a free-text term.
	p.i++
	return Condition{Op: OpHas, Value: t.text}, nil
}

func parseOp(s string) (Op, error) {
	switch s {
	case "=":
		return OpEq, nil
	case "!=":
		return OpNeq, nil
	case ":":
		return OpHas, nil
	case "<=":
		return OpLte, nil
	case ">=":
		return OpGte, nil
	case "<":
		return OpLt, nil
	case ">":
		return OpGt, nil
	default:
		return 0, fmt.Errorf("%w: unsupported filter operator %q", ErrInvalid, s)
	}
}

// token is a lexed unit: a value (bareword or quoted string) or an operator.
type token struct {
	text   string
	op     bool // true for =, !=, :, <, <=, >, >=
	quoted bool // true when the value came from a quoted string literal
	paren  byte // '(' or ')' for a parenthesis, else 0
	// negates marks a "-" written right before a quoted string or a
	// parenthesis, which it negates.
	negates bool
}

// tokenize splits a filter expression into value and operator tokens, honoring
// double-quoted string literals (which may contain spaces and operators).
func tokenize(s string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(s) {
		c := s[i]
		switch c {
		case ' ', '\t':
			i++
		case '"':
			j := i + 1
			var b strings.Builder
			for j < len(s) && s[j] != '"' {
				b.WriteByte(s[j])
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("%w: unterminated quoted string in filter", ErrInvalid)
			}
			toks = append(toks, token{text: b.String(), quoted: true})
			i = j + 1
		case '=', ':':
			toks = append(toks, token{text: string(c), op: true})
			i++
		case '(', ')':
			toks = append(toks, token{text: string(c), paren: c})
			i++
		case '!':
			if i+1 < len(s) && s[i+1] == '=' {
				toks = append(toks, token{text: "!=", op: true})
				i += 2
			} else {
				return nil, fmt.Errorf("%w: unexpected %q in filter (did you mean !=?)", ErrInvalid, "!")
			}
		case '<', '>':
			if i+1 < len(s) && s[i+1] == '=' {
				toks = append(toks, token{text: string(c) + "=", op: true})
				i += 2
			} else {
				toks = append(toks, token{text: string(c), op: true})
				i++
			}
		default:
			j := i
			for j < len(s) && !isDelim(s[j]) {
				j++
			}
			// A lone "-" glued to a quoted string or a parenthesis
			// negates it: -"two words", -(a OR b).
			glued := s[i:j] == "-" && j < len(s) && (s[j] == '"' || s[j] == '(')
			toks = append(toks, token{text: s[i:j], negates: glued})
			i = j
		}
	}
	return toks, nil
}

func isDelim(c byte) bool {
	return c == ' ' || c == '\t' || c == '=' || c == ':' || c == '!' || c == '"' || c == '<' || c == '>' || c == '(' || c == ')'
}

// --- JSON attribute paths -----------------------------------------------------

// JSONPath is a filter field resolved to a path under a KindJSON field.
type JSONPath struct {
	// Field is the KindJSON field's spec.
	Field FieldSpec
	// Path is the path below it, one key per element.
	Path []string
	// Kind is the path's declared kind (FieldSpec.JSONKeys), or KindJSON when
	// it declares none.
	Kind Kind
}

// Key is the path in its dotted form ("view.sea").
func (p JSONPath) Key() string { return strings.Join(p.Path, ".") }

// LookupJSON resolves field to a path under one of spec's KindJSON fields
// ("attributes.view.sea" under "attributes"). ok is false for a field that is
// not one. A path segment may hold letters, digits, '_' and '-'; anything else
// is rejected with ErrInvalid, so a path never carries syntax into a query.
func LookupJSON(spec Spec, field string) (JSONPath, bool, error) {
	head, rest, dotted := strings.Cut(field, ".")
	if !dotted {
		return JSONPath{}, false, nil
	}
	f, ok := spec.Fields[head]
	if !ok || f.Kind != KindJSON {
		return JSONPath{}, false, nil
	}
	path := strings.Split(rest, ".")
	for _, seg := range path {
		if seg == "" {
			return JSONPath{}, false, fmt.Errorf("%w: %q has an empty path segment", ErrInvalid, field)
		}
		for _, r := range seg {
			if !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
				return JSONPath{}, false, fmt.Errorf("%w: %q: a path segment may only hold letters, digits, '_' and '-'", ErrInvalid, field)
			}
		}
	}
	kind, declared := f.JSONKeys[rest]
	if !declared {
		kind = KindJSON
	}
	return JSONPath{Field: f, Path: path, Kind: kind}, true, nil
}

// JSONValue is the typed value c compares the path with: parsed as the
// path's declared kind, or -- undeclared -- a quoted value as a string and an
// unquoted one as the number, boolean or null it reads as, else a string.
func JSONValue(p JSONPath, c Condition) (any, error) {
	return jsonScalar(p, c.Value, c.Quoted)
}

func jsonScalar(p JSONPath, v string, quoted bool) (any, error) {
	switch p.Kind {
	case KindJSON:
		if quoted {
			return v, nil
		}
		switch v {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null":
			return nil, nil
		}
		if n, err := strconv.ParseFloat(v, 64); err == nil && !strings.ContainsAny(v, "xXnN") {
			if i, err := strconv.ParseInt(v, 10, 64); err == nil {
				return i, nil
			}
			return n, nil
		}
		return v, nil
	case KindInt:
		return ParseInt(v)
	case KindFloat:
		return ParseFloat(v)
	case KindBool:
		return ParseBool(v)
	case KindDate:
		if _, err := ParseDate(v); err != nil {
			return nil, err
		}
		return v, nil
	case KindTimestamp:
		t, err := ParseTimestamp(v)
		if err != nil {
			return nil, err
		}
		return t.UTC().Format(time.RFC3339Nano), nil
	default:
		return v, nil
	}
}

// JSONValues is JSONValue over OpIn's alternatives.
func JSONValues(p JSONPath, c Condition) ([]any, error) {
	out := make([]any, 0, len(c.Values))
	for _, v := range c.Values {
		x, err := jsonScalar(p, v, c.Quoted)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}

// JSONNest wraps v in the objects path names, innermost last:
// JSONNest(["view","sea"], true) is {"view": {"sea": true}}, the document a
// containment test (jsonb @>) matches.
func JSONNest(path []string, v any) map[string]any {
	out := map[string]any{path[len(path)-1]: v}
	for i := len(path) - 2; i >= 0; i-- {
		out = map[string]any{path[i]: out}
	}
	return out
}

// JSONPathText renders path as a SQL/JSON path ($."view"."sea"). Segments are
// already restricted by LookupJSON, so quoting each is enough.
func JSONPathText(path []string) string {
	var b strings.Builder
	b.WriteString("$")
	for _, seg := range path {
		b.WriteString(`."`)
		b.WriteString(seg)
		b.WriteString(`"`)
	}
	return b.String()
}

// IsPresenceTest reports whether c is `path:*`, which asks only whether the
// path is there.
func IsPresenceTest(c Condition) bool { return c.Op == OpHas && c.Value == "*" && !c.Quoted }

// --- value normalization and validation ---------------------------------------

// NormalizeEnum maps a filter value onto the stored enum form: uppercase with
// the proto value prefix stripped, so both "ROOM" and "UNIT_TYPE_ROOM" match.
func NormalizeEnum(f FieldSpec, v string) string {
	return strings.TrimPrefix(strings.ToUpper(v), f.EnumPrefix)
}

// ParseDate validates a YYYY-MM-DD filter value.
func ParseDate(v string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %q is not a date (want YYYY-MM-DD)", ErrInvalid, v)
	}
	return t, nil
}

// ParseTimestamp validates an RFC 3339 filter value, accepting a bare date as
// its midnight-UTC instant.
func ParseTimestamp(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%w: %q is not a timestamp (want RFC 3339)", ErrInvalid, v)
}

// ParseInt validates an integral filter value.
func ParseInt(v string) (int64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not an integer", ErrInvalid, v)
	}
	return n, nil
}

// ParseFloat validates a numeric filter value.
func ParseFloat(v string) (float64, error) {
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not a number", ErrInvalid, v)
	}
	return n, nil
}

// ParseBool validates a boolean filter value.
func ParseBool(v string) (bool, error) {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%w: %q is not a boolean", ErrInvalid, v)
	}
	return b, nil
}

// RefID reduces a resource-reference filter value to the bare id a reference
// column stores: the last path segment of an AIP resource name ("users/7" →
// "7"), or the value unchanged when it has no path.
func RefID(v string) string {
	if i := strings.LastIndex(v, "/"); i >= 0 {
		return v[i+1:]
	}
	return v
}

// ContainsPattern builds a case-insensitive "contains" LIKE pattern, escaping
// the LIKE wildcards in the user value so they match literally. Pair it with
// an `ESCAPE '\'` clause (the gorm engine does).
func ContainsPattern(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(v) + "%"
}

// ILikePattern builds the unescaped "contains" pattern GraphQL _ilike uses.
func ILikePattern(v string) string { return "%" + v + "%" }

// CamelCase converts a snake_case column name to the camelCase field name a
// Hasura DDN schema exposes ("display_name" → "displayName").
func CamelCase(snake string) string {
	parts := strings.Split(snake, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

// --- order_by ------------------------------------------------------------------

// OrderTerm is one resolved order_by term: a physical column, a direction, and
// the column's kind (which the keyset cursor needs to round-trip its value).
type OrderTerm struct {
	Column  string
	Desc    bool
	Kind    Kind
	NotNull bool
	// Path, when set, makes the term a declared JSON attribute path under the
	// KindJSON column Column (FieldSpec.JSONKeys): it orders by the value at
	// the path, typed as Kind.
	Path []string
}

// Name is the term's identity in a page token and a row: its column, or for a
// JSON path term the column and the dotted path ("attributes.max_occupancy").
func (t OrderTerm) Name() string {
	if len(t.Path) == 0 {
		return t.Column
	}
	return t.Column + "." + strings.Join(t.Path, ".")
}

// OrderTerms parses an AIP-132 order_by string ("expiry_date desc, name")
// against the spec's sort allowlist. Unknown fields and malformed terms are
// rejected with ErrInvalid.
//
// A path under a KindJSON field sorts when FieldSpec.JSONKeys declares it
// ((store.v1.query).json_keys): "attributes.max_occupancy desc" orders by the
// value at the path, compared as its declared kind. An undeclared path is
// rejected with ErrInvalid, since its values have no one type to order by.
//
// The spec's primary key is appended as the final term (see [Spec.PK]), so the
// result is always a total order — including for the empty order_by, which
// would otherwise page over an unordered result. A caller-supplied term already
// on a PK column is left as-is rather than duplicated, keeping the caller's
// chosen direction.
func OrderTerms(spec Spec, orderBy string) ([]OrderTerm, error) {
	orderBy = strings.TrimSpace(orderBy)
	if orderBy == "" {
		return pkTerms(spec, nil), nil
	}
	parts := strings.Split(orderBy, ",")
	terms := make([]OrderTerm, 0, len(parts)+len(spec.PK))
	for _, part := range parts {
		fields := strings.Fields(part)
		if len(fields) == 0 || len(fields) > 2 {
			return nil, fmt.Errorf("%w: malformed order_by term %q", ErrInvalid, strings.TrimSpace(part))
		}
		term, err := sortTerm(spec, fields[0])
		if err != nil {
			return nil, err
		}
		if len(fields) == 2 {
			switch strings.ToLower(fields[1]) {
			case "asc":
			case "desc":
				term.Desc = true
			default:
				return nil, fmt.Errorf("%w: invalid sort direction %q", ErrInvalid, fields[1])
			}
		}
		terms = append(terms, term)
	}
	return pkTerms(spec, terms), nil
}

// sortTerm resolves one order_by field: an entry of the sort allowlist, or a
// declared JSON attribute path.
func sortTerm(spec Spec, field string) (OrderTerm, error) {
	if ss, ok := spec.Sort[field]; ok {
		return OrderTerm{Column: ss.Column, Kind: ss.Kind, NotNull: ss.NotNull}, nil
	}
	p, ok, err := LookupJSON(spec, field)
	if err != nil {
		return OrderTerm{}, err
	}
	if !ok {
		return OrderTerm{}, fmt.Errorf("%w: cannot sort by %q", ErrInvalid, field)
	}
	if p.Kind == KindJSON {
		return OrderTerm{}, fmt.Errorf("%w: cannot sort by %q: only a JSON path declared in (store.v1.query).json_keys sorts", ErrInvalid, field)
	}
	return OrderTerm{Column: p.Field.Column, Kind: p.Kind, Path: p.Path}, nil
}

// pkTerms appends the spec's primary-key columns to terms, skipping any column
// the caller already sorts on. Ascending is the tiebreaker direction: it only
// orders rows the caller's own terms left tied, so the choice is invisible
// except in making the total order stable.
func pkTerms(spec Spec, terms []OrderTerm) []OrderTerm {
	for _, pk := range spec.PK {
		sorted := false
		for _, t := range terms {
			if t.Column == pk.Column && len(t.Path) == 0 {
				sorted = true
				break
			}
		}
		if !sorted {
			terms = append(terms, OrderTerm{Column: pk.Column, Kind: pk.Kind, NotNull: pk.NotNull})
		}
	}
	return terms
}

// --- pagination ------------------------------------------------------------------

// ListInput is the request shape both list engines take: AIP page controls, an
// order_by string, and the parsed filter conditions.
type ListInput struct {
	PageSize  int32
	PageToken string
	OrderBy   string
	Filter    []Condition
}

const (
	defaultPageSize = 50
	maxPageSize     = 1000
)

// PageLimit clamps the requested page size to [1, maxPageSize], defaulting when
// unset.
func PageLimit(in ListInput) int {
	limit := int(in.PageSize)
	switch {
	case limit <= 0:
		return defaultPageSize
	case limit > maxPageSize:
		return maxPageSize
	}
	return limit
}

// Cursor is a decoded page token: the order_by key of the previous page's last
// row. Paging resumes at the rows sorting strictly after it.
//
// This is keyset (seek) paging, not OFFSET paging. OFFSET makes PostgreSQL scan
// and discard every skipped row, so page N costs O(N × page size) and deep pages
// crawl; a cursor compares against an indexable key, so every page costs the
// same. It is also stable under concurrent writes: a row inserted before the
// cursor shifts every later row by one position, which silently repeats or skips
// rows when the next page is addressed by position.
//
// Cols is carried alongside Vals so a token minted under one order_by is
// rejected rather than misread when replayed against another.
type Cursor struct {
	Cols []string `json:"c"`
	// Vals holds each column's value in its text form, or nil for SQL NULL.
	Vals []*string `json:"v"`
}

// EncodeCursor mints the opaque next-page token addressing the row whose
// order_by key is vals.
func EncodeCursor(terms []OrderTerm, vals []*string) (string, error) {
	c := Cursor{Cols: make([]string, len(terms)), Vals: vals}
	for i, t := range terms {
		c.Cols[i] = t.Name()
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("filterx: encoding page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeCursor reads a page token back into the values addressed by terms. The
// empty token is the first page and yields nil.
//
// A token that does not decode, or whose columns disagree with terms, is
// rejected with ErrInvalid rather than being treated as the first page: silently
// restarting hands the caller a plausible-looking page that is not the one they
// asked for, and can loop a paging client forever. Tokens minted by the previous
// offset-based scheme fail this check, as does replaying a token under a
// different order_by.
func DecodeCursor(terms []OrderTerm, token string) ([]*string, error) {
	if token == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed page token", ErrInvalid)
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%w: malformed page token", ErrInvalid)
	}
	if len(c.Cols) != len(terms) || len(c.Vals) != len(terms) {
		return nil, fmt.Errorf("%w: page token does not match this order_by", ErrInvalid)
	}
	for i, t := range terms {
		if c.Cols[i] != t.Name() {
			return nil, fmt.Errorf("%w: page token does not match this order_by", ErrInvalid)
		}
	}
	return c.Vals, nil
}

// FieldNamer reports the identifier a row type uses for one of its fields: the
// gorm tag's column for a model, the JSON name for a GraphQL response type. ""
// means the field does not hold a column.
type FieldNamer func(reflect.StructField) string

// ColumnKey maps a physical column to the identifier its row type uses for it.
// The two differ on the GraphQL side, where Hasura exposes columns in camelCase.
type ColumnKey func(column string) string

// SameColumn is the [ColumnKey] for row types that name fields by their physical
// column.
func SameColumn(column string) string { return column }

// CursorValues reads the order_by key out of row — the last row of a page — in
// the text form [EncodeCursor] stores. Columns absent from the row type are an
// error rather than a NULL: silently treating a missing sort column as NULL
// would mint a cursor that resumes in the wrong place.
func CursorValues(row any, terms []OrderTerm, keyOf ColumnKey, nameOf FieldNamer) ([]*string, error) {
	rv := reflect.Indirect(reflect.ValueOf(row))
	if rv.Kind() != reflect.Struct {
		return nil, fmt.Errorf("filterx: cannot read a page cursor from %T", row)
	}
	byName := map[string]reflect.Value{}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		if name := nameOf(rt.Field(i)); name != "" {
			byName[name] = rv.Field(i)
		}
	}
	out := make([]*string, len(terms))
	for i, t := range terms {
		fv, ok := byName[keyOf(t.Column)]
		if !ok {
			return nil, fmt.Errorf("filterx: %T has no field for sort column %q", row, t.Column)
		}
		if len(t.Path) > 0 {
			v, err := jsonCursorValue(fv, t.Path)
			if err != nil {
				return nil, fmt.Errorf("filterx: sort column %q: %w", t.Name(), err)
			}
			out[i] = v
			continue
		}
		out[i] = formatCursorValue(fv, t.Kind)
	}
	return out, nil
}

// jsonCursorValue reads the value at path out of a JSON document field (raw
// JSON bytes or text) in the text form a page token carries, or nil when the
// path holds nothing or null.
func jsonCursorValue(v reflect.Value, path []string) (*string, error) {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil, nil
		}
		v = v.Elem()
	}
	var raw []byte
	switch {
	case v.Kind() == reflect.String:
		raw = []byte(v.String())
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8:
		raw = v.Bytes()
	default:
		b, err := json.Marshal(v.Interface())
		if err != nil {
			return nil, err
		}
		raw = b
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var doc any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	return JSONCursorText(doc, path), nil
}

// JSONCursorText is the value at path in a decoded JSON document in the text
// form a page token carries, or nil when the path holds nothing or null.
func JSONCursorText(doc any, path []string) *string {
	for _, seg := range path {
		m, ok := doc.(map[string]any)
		if !ok {
			return nil
		}
		doc = m[seg]
	}
	if doc == nil {
		return nil
	}
	s := fmt.Sprint(doc)
	return &s
}

// formatCursorValue renders one column value in the text form a page token
// carries, or nil for a NULL (a nil pointer or a nil interface).
func formatCursorValue(v reflect.Value, kind Kind) *string {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	var s string
	switch t := v.Interface().(type) {
	case time.Time:
		// Dates and instants both round-trip through the same textual forms the
		// filter language accepts, so ParseDate/ParseTimestamp recover them.
		if kind == KindDate {
			s = t.Format("2006-01-02")
		} else {
			s = t.Format(time.RFC3339Nano)
		}
	default:
		// Ints, floats, bools and strings all round-trip through their default
		// text form via the Parse* helpers below.
		s = fmt.Sprint(t)
	}
	return &s
}

// ParseCursorValue recovers one cursor value as the typed comparison argument
// its column takes, reusing the same conversions the filter language uses.
func ParseCursorValue(s *string, kind Kind) (any, error) {
	if s == nil {
		return nil, nil
	}
	switch kind {
	case KindTimestamp:
		return ParseTimestamp(*s)
	case KindDate:
		return ParseDate(*s)
	case KindInt:
		return ParseInt(*s)
	case KindFloat:
		return ParseFloat(*s)
	case KindBool:
		return ParseBool(*s)
	default:
		return *s, nil
	}
}

// NextPage trims a limit+1 result page and mints the token addressing the page
// after it. Engines fetch limit+1 rows and hand them here; the extra row is the
// existence probe for a further page, which is why no COUNT is needed.
func NextPage[M any](rows []M, limit int, terms []OrderTerm, keyOf ColumnKey, nameOf FieldNamer) ([]M, string, error) {
	if len(rows) <= limit {
		return rows, "", nil
	}
	page := rows[:limit]
	vals, err := CursorValues(page[len(page)-1], terms, keyOf, nameOf)
	if err != nil {
		return nil, "", err
	}
	token, err := EncodeCursor(terms, vals)
	if err != nil {
		return nil, "", err
	}
	return page, token, nil
}

// --- observability ----------------------------------------------------------------

// Observer receives the engines' trace spans and debug events; wire one with
// the engines' Observe option (engines observe nothing by default). Adapt your
// telemetry runtime to this interface — with the telemetry opt, a ready-made
// telemetry adapter (TelemetryObserver) is generated in this package.
type Observer interface {
	// Span wraps one engine operation (e.g. a list fetch) in a trace span.
	Span(ctx context.Context, name string, fn func(context.Context) error) error
	// Debug reports a non-fatal engine event (e.g. a rejected filter).
	Debug(msg string, kv map[string]any)
}

// NopObserver observes nothing.
type NopObserver struct{}

// Span runs fn without tracing.
func (NopObserver) Span(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

// Debug discards the event.
func (NopObserver) Debug(string, map[string]any) {}
