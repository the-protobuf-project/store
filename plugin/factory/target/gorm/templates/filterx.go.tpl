{{.Header}}

// Package filterx is the generated filter/order/list SDK: the Condition
// vocabulary an AIP-160 filter parses into, the Spec data model the per-schema
// filters.go files instantiate, the shared semantics (enum normalization,
// date/number validation, ILIKE escaping, sort allowlists, page math), and the
// two chainable engines built from a Spec — Gorm[M] for SQL and Hasura[M] for
// GraphQL. Engines are code, specs are data: one Spec per table drives both
// backends, so both accept identical filter strings by construction.
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
)

// Condition is one parsed term of a filter expression. A condition with an
// empty Field is a bareword free-text term, matched against the spec's Search
// columns. Conditions in a slice are AND-combined.
type Condition struct {
	Field string
	Op    Op
	Value string
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
)

// FieldSpec describes one filterable field: its physical column, its kind, and
// (for enums) the proto value prefix stripped during normalization.
type FieldSpec struct {
	Column     string
	Kind       Kind
	EnumPrefix string
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

// Parse parses a pragmatic subset of the AIP-160 filter language into a flat,
// AND-combined list of conditions. It supports:
//
//   - `field = value`, `field != value`, `field : value`
//   - `field <= value`, `field >= value` (ordered fields, e.g. dates)
//   - quoted values ("two words") and barewords (SUMMER25)
//   - a bare term with no operator, treated as a free-text search
//   - terms separated by whitespace or an explicit `AND`
//
// It validates only syntax; the engines validate each Field against the spec.
// A malformed expression yields ErrInvalid. The empty string parses to no
// conditions (an unfiltered list).
func Parse(filter string) ([]Condition, error) {
	toks, err := tokenize(filter)
	if err != nil {
		return nil, err
	}

	var conds []Condition
	for i := 0; i < len(toks); {
		t := toks[i]
		if t.op {
			return nil, fmt.Errorf("%w: unexpected operator %q in filter", ErrInvalid, t.text)
		}
		if strings.EqualFold(t.text, "AND") && !t.quoted {
			i++
			continue
		}
		// A value token followed by an operator is a `field op value` term.
		if i+1 < len(toks) && toks[i+1].op {
			if i+2 >= len(toks) || toks[i+2].op {
				return nil, fmt.Errorf("%w: filter operator %q is missing a value", ErrInvalid, toks[i+1].text)
			}
			op, err := parseOp(toks[i+1].text)
			if err != nil {
				return nil, err
			}
			conds = append(conds, Condition{Field: t.text, Op: op, Value: toks[i+2].text})
			i += 3
			continue
		}
		// Otherwise it's a free-text term.
		conds = append(conds, Condition{Op: OpHas, Value: t.text})
		i++
	}
	return conds, nil
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
	default:
		return 0, fmt.Errorf("%w: unsupported filter operator %q", ErrInvalid, s)
	}
}

// token is a lexed unit: a value (bareword or quoted string) or an operator.
type token struct {
	text   string
	op     bool // true for =, !=, :, <=, >=
	quoted bool // true when the value came from a quoted string literal
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
				return nil, fmt.Errorf("%w: unexpected %q in filter (only <= and >= are supported)", ErrInvalid, string(c))
			}
		default:
			j := i
			for j < len(s) && !isDelim(s[j]) {
				j++
			}
			toks = append(toks, token{text: s[i:j]})
			i = j
		}
	}
	return toks, nil
}

func isDelim(c byte) bool {
	return c == ' ' || c == '\t' || c == '=' || c == ':' || c == '!' || c == '"' || c == '<' || c == '>'
}

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
}

// OrderTerms parses an AIP-132 order_by string ("expiry_date desc, name")
// against the spec's sort allowlist. Unknown fields and malformed terms are
// rejected with ErrInvalid.
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
		ss, ok := spec.Sort[fields[0]]
		if !ok {
			return nil, fmt.Errorf("%w: cannot sort by %q", ErrInvalid, fields[0])
		}
		term := OrderTerm{Column: ss.Column, Kind: ss.Kind, NotNull: ss.NotNull}
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

// pkTerms appends the spec's primary-key columns to terms, skipping any column
// the caller already sorts on. Ascending is the tiebreaker direction: it only
// orders rows the caller's own terms left tied, so the choice is invisible
// except in making the total order stable.
func pkTerms(spec Spec, terms []OrderTerm) []OrderTerm {
	for _, pk := range spec.PK {
		sorted := false
		for _, t := range terms {
			if t.Column == pk.Column {
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
		c.Cols[i] = t.Column
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
		if c.Cols[i] != t.Column {
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
		out[i] = formatCursorValue(fv, t.Kind)
	}
	return out, nil
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
// opentelementry adapter (OpentelementryObserver) is generated in this package.
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
