{{.Header}}

// gorm.go is the GORM engine: Gorm[M] builds a chainable engine over a Spec
// that translates parsed AIP-160 conditions into SQL predicates, resolves
// AIP-132 order_by against the spec's sort allowlist, and runs the paginated
// list (limit+1 with an opaque offset token). The caller passes a *gorm.DB
// already scoped to the parent and carrying its preloads; the engine only adds
// filter/order/pagination.

package filterx

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"gorm.io/gorm"
)

// SQLHandler overrides (or extends) the engine's dispatch for one filter
// field — the escape hatch for derived predicates the schema cannot express
// (e.g. a state computed from other columns via subqueries).
type SQLHandler func(c Condition) (clause string, args []any, err error)

// GormEngine is the SQL filter/order/list engine for one model. Build it with
// Gorm and tune it with the chainable Override/Observe options.
type GormEngine[M any] struct {
	spec      Spec
	overrides map[string]SQLHandler
	observer  Observer
}

// Gorm builds the SQL engine for spec:
//
//	rows, next, err := filterx.Gorm[property.Unit](property.UnitFilterSpec).
//		List(ctx, scopedDB, in)
func Gorm[M any](spec Spec) *GormEngine[M] {
	return &GormEngine[M]{spec: spec, observer: NopObserver{}}
}

// Override installs a custom handler for one filter field. A handler wins over
// the spec for its field and may serve fields the spec does not list.
func (e *GormEngine[M]) Override(field string, h SQLHandler) *GormEngine[M] {
	if e.overrides == nil {
		e.overrides = map[string]SQLHandler{}
	}
	e.overrides[field] = h
	return e
}

// Observe routes the engine's spans and debug events to o.
func (e *GormEngine[M]) Observe(o Observer) *GormEngine[M] {
	e.observer = o
	return e
}

// Where translates the conditions (AND-combined) into one SQL clause with
// positional args. Unknown fields, unsupported operators, and malformed values
// are rejected with ErrInvalid. Empty conditions yield an empty clause.
func (e *GormEngine[M]) Where(conds []Condition) (string, []any, error) {
	var (
		clauses []string
		args    []any
	)
	for _, c := range conds {
		cl, a, err := e.condition(c)
		if err != nil {
			return "", nil, err
		}
		clauses, args = append(clauses, cl), append(args, a...)
	}
	return strings.Join(clauses, " AND "), args, nil
}

// condition translates one condition, its overrides applying at any depth
// of OR, AND and NOT.
func (e *GormEngine[M]) condition(c Condition) (string, []any, error) {
	if IsComposite(c) {
		return sqlComposite(c, e.condition)
	}
	if h, ok := e.overrides[c.Field]; ok {
		return h(c)
	}
	return sqlCondition(e.spec, c)
}

// sqlComposite translates an OpOr, OpAnd or OpNot condition, each operand
// through each. An empty OR matches nothing and an empty AND everything.
// NOT holds where its operand does not hold, including where the operand is
// NULL (a comparison with a NULL column), as a document missing the field
// matches a must_not on OpenSearch.
func sqlComposite(c Condition, each func(Condition) (string, []any, error)) (string, []any, error) {
	parts := make([]string, 0, len(c.Any))
	var args []any
	for _, alt := range c.Any {
		cl, a, err := each(alt)
		if err != nil {
			return "", nil, err
		}
		parts, args = append(parts, cl), append(args, a...)
	}
	switch {
	case c.Op == OpOr && len(parts) == 0:
		return "FALSE", nil, nil
	case c.Op == OpOr:
		return "(" + strings.Join(parts, " OR ") + ")", args, nil
	case len(parts) == 0:
		parts = []string{"TRUE"}
	}
	and := "(" + strings.Join(parts, " AND ") + ")"
	if c.Op == OpNot {
		return "NOT coalesce(" + and + ", false)", args, nil
	}
	return and, args, nil
}

// OrderClause resolves an order_by string into a safe "col DIR, ..." clause
// via the spec's sort allowlist. Empty order_by still yields the spec's
// primary-key clause, which [OrderTerms] appends to keep paging stable.
func (e *GormEngine[M]) OrderClause(orderBy string) (string, error) {
	terms, err := OrderTerms(e.spec, orderBy)
	if err != nil {
		return "", err
	}
	return orderClause(terms), nil
}

// orderClause renders resolved terms as the "col DIR, ..." clause.
func orderClause(terms []OrderTerm) string {
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		dir := "ASC"
		if t.Desc {
			dir = "DESC"
		}
		col := t.Column
		if len(t.Path) > 0 {
			col = sqlTermExpr(`"`+t.Column+`"`, t)
		}
		parts = append(parts, col+" "+dir)
	}
	return strings.Join(parts, ", ")
}

// qualifyCol renders a column reference qualified by the spec's table.
func qualifyCol(spec Spec, col string) string { return spec.Table + `."` + col + `"` }

// SQLTermExpr is the SQL expression a resolved order term sorts by, qualified
// by the spec's table: the column, or for a JSON path term the value at the
// path cast to its declared kind -- ("t"."attributes" ->> 'max_occupancy')::bigint.
// A value at the path that does not read as that kind fails the query, as
// the declaration says it cannot happen.
func SQLTermExpr(spec Spec, t OrderTerm) string { return sqlTermExpr(qualifyCol(spec, t.Column), t) }

// sqlTermExpr is SQLTermExpr over the column reference col. LookupJSON admits
// only [A-Za-z0-9_-] path segments, so the path is safe inline.
func sqlTermExpr(col string, t OrderTerm) string {
	if len(t.Path) == 0 {
		return col
	}
	text := "(" + col + " ->> '" + t.Path[0] + "')"
	if len(t.Path) > 1 {
		text = "(" + col + " #>> '{" + strings.Join(t.Path, ",") + "}')"
	}
	switch t.Kind {
	case KindInt:
		return "(" + text + "::bigint)"
	case KindFloat:
		return "(" + text + "::double precision)"
	case KindBool:
		return "(" + text + "::boolean)"
	case KindDate:
		return "(" + text + "::date)"
	case KindTimestamp:
		return "(" + text + "::timestamptz)"
	default:
		return text
	}
}

// Seek renders the keyset predicate resuming after pageToken, under the ordering
// orderBy resolves to — the clause List ANDs in to fetch the next page. The
// empty token is the first page and yields an empty clause; a token that does
// not match this order_by is rejected with ErrInvalid. Exposed alongside Where
// and OrderClause so a caller composing its own query pages the same way List
// does.
func (e *GormEngine[M]) Seek(orderBy, pageToken string) (string, []any, error) {
	terms, err := OrderTerms(e.spec, orderBy)
	if err != nil {
		return "", nil, err
	}
	cursor, err := DecodeCursor(terms, pageToken)
	if err != nil {
		return "", nil, err
	}
	return keysetWhere(e.spec, terms, cursor)
}

// keysetWhere renders the "rows strictly after the cursor" predicate. An empty
// cursor is the first page and yields no clause.
//
// A row-value comparison — (a, b) > (?, ?) — would be shorter, but PostgreSQL
// only accepts it when every term sorts the same way, and order_by permits mixed
// directions. So the comparison is written in its expanded lexicographic form:
// one branch per term, matching the rows that tie on every earlier term and sort
// strictly after on this one.
//
// NULLs follow PostgreSQL's defaults for each direction — ASC sorts them last,
// DESC first — because that is the order the query itself runs with. Getting
// this wrong does not error; it silently drops the rows on the null side of the
// cursor, since any comparison with NULL is NULL rather than true.
func keysetWhere(spec Spec, terms []OrderTerm, cursor []*string) (string, []any, error) {
	if cursor == nil {
		return "", nil, nil
	}
	var branches []string
	var args []any
	for i, t := range terms {
		after, afterArgs, err := keysetAfter(spec, t, cursor[i])
		if err != nil {
			return "", nil, err
		}
		if after == "" {
			// Nothing sorts strictly after this value on this term (an ASC NULL is
			// already last), so only the tie branch continues into later terms.
			continue
		}
		parts := make([]string, 0, i+1)
		branchArgs := make([]any, 0, i+1)
		for j := 0; j < i; j++ {
			eq, eqArgs, err := keysetEq(spec, terms[j], cursor[j])
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, eq)
			branchArgs = append(branchArgs, eqArgs...)
		}
		parts = append(parts, after)
		branchArgs = append(branchArgs, afterArgs...)
		if len(parts) == 1 {
			branches = append(branches, parts[0])
		} else {
			branches = append(branches, "("+strings.Join(parts, " AND ")+")")
		}
		args = append(args, branchArgs...)
	}
	if len(branches) == 0 {
		return "", nil, nil
	}
	return "(" + strings.Join(branches, " OR ") + ")", args, nil
}

// keysetEq matches rows tying with the cursor on one term.
func keysetEq(spec Spec, t OrderTerm, v *string) (string, []any, error) {
	col := SQLTermExpr(spec, t)
	if v == nil {
		return col + " IS NULL", nil, nil
	}
	arg, err := ParseCursorValue(v, t.Kind)
	if err != nil {
		return "", nil, err
	}
	return col + " = ?", []any{arg}, nil
}

// keysetAfter matches rows sorting strictly after the cursor on one term.
// Returns "" when no row can: an ASC NULL already sorts last.
func keysetAfter(spec Spec, t OrderTerm, v *string) (string, []any, error) {
	col := SQLTermExpr(spec, t)
	if v == nil {
		if t.Desc {
			return col + " IS NOT NULL", nil, nil // DESC sorts NULLs first
		}
		return "", nil, nil
	}
	arg, err := ParseCursorValue(v, t.Kind)
	if err != nil {
		return "", nil, err
	}
	if t.Desc {
		return col + " < ?", []any{arg}, nil
	}
	if t.NotNull {
		return col + " > ?", []any{arg}, nil
	}
	// ASC sorts NULLs last, so they fall after every non-null value.
	return "(" + col + " > ? OR " + col + " IS NULL)", []any{arg}, nil
}

// GormColumn resolves the physical column a model field maps to, reading the
// column: fragment the models target writes into every field's gorm tag.
func GormColumn(f reflect.StructField) string {
	tag, ok := f.Tag.Lookup("gorm")
	if !ok {
		return ""
	}
	for _, part := range strings.Split(tag, ";") {
		if col, found := strings.CutPrefix(part, "column:"); found {
			return col
		}
	}
	return ""
}

// List runs the paginated list: it applies the spec-driven filter and order to
// q (already parent-scoped and preloaded by the caller), fetches limit+1 rows,
// and mints the opaque next-page token. Invalid filter/order input is rejected
// with ErrInvalid before any query runs.
func (e *GormEngine[M]) List(ctx context.Context, q *gorm.DB, in ListInput) ([]M, string, error) {
	terms, err := OrderTerms(e.spec, in.OrderBy)
	if err != nil {
		e.observer.Debug("rejected order_by", map[string]any{"table": e.spec.Table, "order_by": in.OrderBy, "error": err.Error()})
		return nil, "", err
	}
	where, args, err := e.Where(in.Filter)
	if err != nil {
		e.observer.Debug("rejected filter", map[string]any{"table": e.spec.Table, "error": err.Error()})
		return nil, "", err
	}
	cursor, err := DecodeCursor(terms, in.PageToken)
	if err != nil {
		e.observer.Debug("rejected page_token", map[string]any{"table": e.spec.Table, "error": err.Error()})
		return nil, "", err
	}
	seek, seekArgs, err := keysetWhere(e.spec, terms, cursor)
	if err != nil {
		e.observer.Debug("rejected page_token", map[string]any{"table": e.spec.Table, "error": err.Error()})
		return nil, "", err
	}

	limit := PageLimit(in)
	q = q.WithContext(ctx).Limit(limit + 1)
	if order := orderClause(terms); order != "" {
		q = q.Order(order)
	}
	if where != "" {
		q = q.Where(where, args...)
	}
	if seek != "" {
		q = q.Where(seek, seekArgs...)
	}

	var rows []M
	err = e.observer.Span(ctx, "filterx.List/"+e.spec.Table, func(ctx context.Context) error {
		return q.WithContext(ctx).Find(&rows).Error
	})
	if err != nil {
		return nil, "", err
	}
	return NextPage(rows, limit, terms, SameColumn, GormColumn)
}

// sqlCondition translates one spec-listed condition.
func sqlCondition(spec Spec, c Condition) (string, []any, error) {
	if IsComposite(c) {
		return sqlComposite(c, func(alt Condition) (string, []any, error) { return sqlCondition(spec, alt) })
	}
	if c.Field == "" {
		return sqlSearchCondition(spec, c)
	}
	f, ok := spec.Fields[c.Field]
	if !ok {
		p, isPath, err := LookupJSON(spec, c.Field)
		if err != nil {
			return "", nil, err
		}
		if !isPath {
			return "", nil, fmt.Errorf("%w: cannot filter by %q", ErrInvalid, c.Field)
		}
		return sqlJSONCondition(spec.Table+`."`+p.Field.Column+`"`, p, c)
	}
	col := spec.Table + `."` + f.Column + `"`
	if c.Op == OpNull {
		null, err := ParseBool(c.Value)
		if err != nil {
			return "", nil, err
		}
		if null {
			return col + " IS NULL", nil, nil
		}
		return col + " IS NOT NULL", nil, nil
	}
	if f.Via != nil {
		// A value-object field: match the referenced row, then point at it.
		inner := Spec{Table: f.Via.Table, Fields: map[string]FieldSpec{
			c.Field: {Column: f.Via.Column, Kind: f.Kind, EnumPrefix: f.EnumPrefix},
		}}
		cl, a, err := sqlCondition(inner, c)
		if err != nil {
			return "", nil, err
		}
		return col + ` IN (SELECT ` + f.Via.Table + `."` + f.Via.Key + `" FROM ` + f.Via.Table + ` WHERE ` + cl + `)`, a, nil
	}
	if c.Op == OpIn {
		return sqlIn(f, col, c)
	}
	switch f.Kind {
	case KindText:
		switch c.Op {
		case OpEq:
			return col + " = ?", []any{c.Value}, nil
		case OpNeq:
			return col + " <> ?", []any{c.Value}, nil
		case OpHas:
			return col + ` ILIKE ? ESCAPE '\'`, []any{ContainsPattern(c.Value)}, nil
		}
	case KindEnum:
		v := NormalizeEnum(f, c.Value)
		switch c.Op {
		case OpEq:
			return col + " = ?", []any{v}, nil
		case OpNeq:
			return col + " <> ?", []any{v}, nil
		}
	case KindRef:
		switch c.Op {
		case OpEq:
			return col + " = ?", []any{RefID(c.Value)}, nil
		case OpNeq:
			return col + " <> ?", []any{RefID(c.Value)}, nil
		}
	case KindDate:
		v, err := ParseDate(c.Value)
		if err != nil {
			return "", nil, err
		}
		if op, ok := sqlOrderedOp(c.Op); ok {
			return col + " " + op + " ?", []any{v}, nil
		}
	case KindTimestamp:
		v, err := ParseTimestamp(c.Value)
		if err != nil {
			return "", nil, err
		}
		if op, ok := sqlOrderedOp(c.Op); ok {
			return col + " " + op + " ?", []any{v}, nil
		}
	case KindInt:
		v, err := ParseInt(c.Value)
		if err != nil {
			return "", nil, err
		}
		if op, ok := sqlNumericOp(c.Op); ok {
			return col + " " + op + " ?", []any{v}, nil
		}
	case KindFloat:
		v, err := ParseFloat(c.Value)
		if err != nil {
			return "", nil, err
		}
		if op, ok := sqlNumericOp(c.Op); ok {
			return col + " " + op + " ?", []any{v}, nil
		}
	case KindBool:
		v, err := ParseBool(c.Value)
		if err != nil {
			return "", nil, err
		}
		switch c.Op {
		case OpEq:
			return col + " = ?", []any{v}, nil
		case OpNeq:
			return col + " <> ?", []any{v}, nil
		}
	case KindTags:
		if c.Op == OpHas {
			return col + " @> ARRAY[?]", []any{c.Value}, nil
		}
	}
	return "", nil, fmt.Errorf("%w: unsupported operator for %q", ErrInvalid, c.Field)
}

// sqlJSONCondition translates a condition on a path under a jsonb column.
// Equality and membership are containment (@>), which a GIN index on the
// column serves; presence, null and the ordered comparisons are SQL/JSON path
// tests (jsonb_path_exists), which it does not. The path travels as a bound
// jsonpath argument and the value as bound jsonb, never as SQL text. (The
// jsonb ? and @? operators are not used: GORM would read their '?' as a
// placeholder.)
func sqlJSONCondition(col string, p JSONPath, c Condition) (string, []any, error) {
	path := JSONPathText(p.Path)
	contains := func(v any) (string, []any, error) {
		doc, err := json.Marshal(JSONNest(p.Path, v))
		if err != nil {
			return "", nil, fmt.Errorf("%w: %q: %v", ErrInvalid, c.Field, err)
		}
		return col + " @> ?::jsonb", []any{string(doc)}, nil
	}
	switch c.Op {
	case OpNull:
		null, err := ParseBool(c.Value)
		if err != nil {
			return "", nil, err
		}
		if null {
			return "NOT jsonb_path_exists(" + col + ", ?::jsonpath)", []any{path}, nil
		}
		return "jsonb_path_exists(" + col + ", ?::jsonpath)", []any{path}, nil
	case OpEq, OpNeq:
		v, err := JSONValue(p, c)
		if err != nil {
			return "", nil, err
		}
		cl, args, err := contains(v)
		if err != nil || c.Op == OpEq {
			return cl, args, err
		}
		return "NOT (" + cl + ")", args, nil
	case OpHas:
		if IsPresenceTest(c) {
			return "jsonb_path_exists(" + col + ", ?::jsonpath)", []any{path}, nil
		}
		v, err := JSONValue(p, c)
		if err != nil {
			return "", nil, err
		}
		// The value itself, or an element of an array at the path: jsonb
		// containment only reaches into an array for an array.
		one, a1, err := contains(v)
		if err != nil {
			return "", nil, err
		}
		many, a2, err := contains([]any{v})
		if err != nil {
			return "", nil, err
		}
		return "(" + one + " OR " + many + ")", append(a1, a2...), nil
	case OpIn:
		vals, err := JSONValues(p, c)
		if err != nil {
			return "", nil, err
		}
		if len(vals) == 0 {
			return "FALSE", nil, nil
		}
		parts := make([]string, 0, len(vals))
		var args []any
		for _, v := range vals {
			cl, a, err := contains(v)
			if err != nil {
				return "", nil, err
			}
			parts, args = append(parts, cl), append(args, a...)
		}
		return "(" + strings.Join(parts, " OR ") + ")", args, nil
	case OpLt, OpLte, OpGt, OpGte:
		v, err := JSONValue(p, c)
		if err != nil {
			return "", nil, err
		}
		if v == nil {
			return "", nil, fmt.Errorf("%w: %q cannot be compared with null", ErrInvalid, c.Field)
		}
		op, _ := sqlOrderedOp(c.Op)
		arg, err := json.Marshal(v)
		if err != nil {
			return "", nil, fmt.Errorf("%w: %q: %v", ErrInvalid, c.Field, err)
		}
		return "jsonb_path_exists(" + col + ", ?::jsonpath, jsonb_build_object('v', ?::jsonb))",
			[]any{path + " ? (@ " + jsonPathOp(op) + " $v)", string(arg)}, nil
	}
	return "", nil, fmt.Errorf("%w: unsupported operator for %q", ErrInvalid, c.Field)
}

// jsonPathOp is the SQL/JSON path spelling of a SQL comparison.
func jsonPathOp(op string) string {
	if op == "=" {
		return "=="
	}
	return op
}

// sqlSearchCondition matches a bareword term against the spec's search columns
// (case-insensitive contains, OR-combined).
func sqlSearchCondition(spec Spec, c Condition) (string, []any, error) {
	if len(spec.Search) == 0 {
		return "", nil, fmt.Errorf("%w: free-text search is not supported here", ErrInvalid)
	}
	pat := ContainsPattern(c.Value)
	parts := make([]string, 0, len(spec.Search))
	args := make([]any, 0, len(spec.Search))
	for _, col := range spec.Search {
		parts = append(parts, spec.Table+`."`+col+`" ILIKE ? ESCAPE '\'`)
		args = append(args, pat)
	}
	if len(parts) == 1 {
		return parts[0], args, nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", args, nil
}

// sqlIn translates an OpIn condition: the values normalised as an equality on
// that kind would be, matched in one IN list. An empty list matches nothing.
func sqlIn(f FieldSpec, col string, c Condition) (string, []any, error) {
	if len(c.Values) == 0 {
		return "FALSE", nil, nil
	}
	vals := make([]any, 0, len(c.Values))
	for _, v := range c.Values {
		switch f.Kind {
		case KindText:
			vals = append(vals, v)
		case KindEnum:
			vals = append(vals, NormalizeEnum(f, v))
		case KindRef:
			vals = append(vals, RefID(v))
		default:
			return "", nil, fmt.Errorf("%w: %q cannot be matched against a list", ErrInvalid, c.Field)
		}
	}
	return col + " IN ?", []any{vals}, nil
}

// sqlOrderedOp maps an Op onto the SQL comparison an ordered kind supports.
func sqlOrderedOp(op Op) (string, bool) {
	switch op {
	case OpEq:
		return "=", true
	case OpLte:
		return "<=", true
	case OpGte:
		return ">=", true
	case OpLt:
		return "<", true
	case OpGt:
		return ">", true
	default:
		return "", false
	}
}

// sqlNumericOp maps an Op onto the SQL comparison a numeric kind supports.
func sqlNumericOp(op Op) (string, bool) {
	switch op {
	case OpNeq:
		return "<>", true
	default:
		return sqlOrderedOp(op)
	}
}
