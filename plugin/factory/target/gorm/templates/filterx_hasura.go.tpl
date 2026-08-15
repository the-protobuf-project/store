{{.Header}}

// hasura.go is the Hasura/GraphQL engine: Hasura[M] builds a chainable engine
// over a Spec (and a generated resource query handler) that translates parsed
// AIP-160 conditions into BoolExp predicates, resolves AIP-132 order_by into
// order terms, and runs the paginated list through the generic QueryHandler
// every generated resource handler satisfies — so any entity plugs in with
// zero per-entity glue. Column names are the camelCase form Hasura DDN
// exposes, derived from the spec's snake_case columns at run time (the same
// Spec drives the gorm engine).

package filterx

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"{{.GraphQLImport}}"
)

// GraphQLHandler overrides (or extends) the engine's dispatch for one filter
// field. Returning an error rejects the condition; a field absent from both
// the spec and the overrides is rejected as unknown — which is also how a
// gorm-only derived field stays deliberately unfilterable on this backend.
type GraphQLHandler func(c Condition) (graphql.Predicate, error)

// HasuraEngine is the GraphQL filter/order/list engine for one resource. Build
// it with Hasura and tune it with the chainable Scope/Override/Observe options.
type HasuraEngine[M any] struct {
	spec      Spec
	query     graphql.QueryHandler[M]
	scope     []graphql.Predicate
	overrides map[string]GraphQLHandler
	observer  Observer
}

// Hasura builds the GraphQL engine for spec over q (any generated resource
// query handler):
//
//	rows, next, err := filterx.Hasura[pschema.PropertyUnits](property.UnitFilterSpec, svc.Query.Property.Units).
//		Scope(unitsql.PropertyId.Eq(propertyID)).
//		List(ctx, in)
func Hasura[M any](spec Spec, q graphql.QueryHandler[M]) *HasuraEngine[M] {
	return &HasuraEngine[M]{spec: spec, query: q, observer: NopObserver{}}
}

// Scope ANDs fixed predicates into every query — the parent scoping
// (e.g. propertyId = X) the caller owns.
func (e *HasuraEngine[M]) Scope(preds ...graphql.Predicate) *HasuraEngine[M] {
	e.scope = append(e.scope, preds...)
	return e
}

// Override installs a custom handler for one filter field.
func (e *HasuraEngine[M]) Override(field string, h GraphQLHandler) *HasuraEngine[M] {
	if e.overrides == nil {
		e.overrides = map[string]GraphQLHandler{}
	}
	e.overrides[field] = h
	return e
}

// Observe routes the engine's spans and debug events to o.
func (e *HasuraEngine[M]) Observe(o Observer) *HasuraEngine[M] {
	e.observer = o
	return e
}

// Predicate translates the conditions (AND-combined) into one BoolExp
// predicate. The bool reports whether any predicate resulted (false for empty
// conditions). Unknown fields, unsupported operators, and malformed values are
// rejected with ErrInvalid.
func (e *HasuraEngine[M]) Predicate(conds []Condition) (graphql.Predicate, bool, error) {
	preds := make([]graphql.Predicate, 0, len(conds))
	for _, c := range conds {
		if h, ok := e.overrides[c.Field]; ok {
			p, err := h(c)
			if err != nil {
				return graphql.Predicate{}, false, err
			}
			preds = append(preds, p)
			continue
		}
		p, err := gqlCondition(e.spec, c)
		if err != nil {
			return graphql.Predicate{}, false, err
		}
		preds = append(preds, p)
	}
	switch len(preds) {
	case 0:
		return graphql.Predicate{}, false, nil
	case 1:
		return preds[0], true, nil
	default:
		return graphql.And(preds...), true, nil
	}
}

// OrderTerms resolves an order_by string into GraphQL order terms via the
// spec's sort allowlist. Empty order_by still yields the spec's primary-key
// term, which [OrderTerms] appends to keep paging stable.
func (e *HasuraEngine[M]) OrderTerms(orderBy string) ([]graphql.OrderTerm, error) {
	terms, err := OrderTerms(e.spec, orderBy)
	if err != nil {
		return nil, err
	}
	return gqlOrderTerms(terms), nil
}

// gqlOrderTerms converts resolved terms to their GraphQL form. Direction is all
// a graphql.OrderTerm carries, so StringField serves every column type.
func gqlOrderTerms(terms []OrderTerm) []graphql.OrderTerm {
	out := make([]graphql.OrderTerm, 0, len(terms))
	for _, t := range terms {
		f := graphql.StringField{Col: CamelCase(t.Column)}
		if t.Desc {
			out = append(out, f.Desc())
		} else {
			out = append(out, f.Asc())
		}
	}
	return out
}

// Seek builds the keyset predicate resuming after pageToken, under the ordering
// orderBy resolves to — the predicate List ANDs in to fetch the next page. The
// empty token is the first page and yields ok=false; a token that does not match
// this order_by is rejected with ErrInvalid. Exposed alongside Predicate and
// OrderTerms so a caller composing its own request pages the same way List does.
func (e *HasuraEngine[M]) Seek(orderBy, pageToken string) (graphql.Predicate, bool, error) {
	terms, err := OrderTerms(e.spec, orderBy)
	if err != nil {
		return graphql.Predicate{}, false, err
	}
	cursor, err := DecodeCursor(terms, pageToken)
	if err != nil {
		return graphql.Predicate{}, false, err
	}
	return keysetPredicate(terms, cursor)
}

// keysetPredicate builds the "rows strictly after the cursor" predicate, the
// GraphQL twin of the gorm engine's keysetWhere — see that function for why the
// comparison is expanded lexicographically rather than written as a row-value
// comparison, and how NULLs are placed. An empty cursor is the first page and
// yields ok=false.
func keysetPredicate(terms []OrderTerm, cursor []*string) (graphql.Predicate, bool, error) {
	if cursor == nil {
		return graphql.Predicate{}, false, nil
	}
	var branches []graphql.Predicate
	for i, t := range terms {
		after, ok, err := keysetAfterPred(t, cursor[i])
		if err != nil {
			return graphql.Predicate{}, false, err
		}
		if !ok {
			continue // an ASC NULL already sorts last; only the tie branch continues
		}
		parts := make([]graphql.Predicate, 0, i+1)
		for j := 0; j < i; j++ {
			eq, err := keysetEqPred(terms[j], cursor[j])
			if err != nil {
				return graphql.Predicate{}, false, err
			}
			parts = append(parts, eq)
		}
		parts = append(parts, after)
		if len(parts) == 1 {
			branches = append(branches, parts[0])
		} else {
			branches = append(branches, graphql.And(parts...))
		}
	}
	switch len(branches) {
	case 0:
		return graphql.Predicate{}, false, nil
	case 1:
		return branches[0], true, nil
	default:
		return graphql.Or(branches...), true, nil
	}
}

// keysetEqPred matches rows tying with the cursor on one term.
func keysetEqPred(t OrderTerm, v *string) (graphql.Predicate, error) {
	col := CamelCase(t.Column)
	if v == nil {
		return graphql.StringField{Col: col}.IsNull(true), nil
	}
	arg, err := ParseCursorValue(v, t.Kind)
	if err != nil {
		return graphql.Predicate{}, err
	}
	return gqlEq(col, t.Kind, arg)
}

// keysetAfterPred matches rows sorting strictly after the cursor on one term.
// ok=false when no row can: an ASC NULL already sorts last.
func keysetAfterPred(t OrderTerm, v *string) (graphql.Predicate, bool, error) {
	col := CamelCase(t.Column)
	f := graphql.StringField{Col: col}
	if v == nil {
		if t.Desc {
			return f.IsNull(false), true, nil // DESC sorts NULLs first
		}
		return graphql.Predicate{}, false, nil
	}
	arg, err := ParseCursorValue(v, t.Kind)
	if err != nil {
		return graphql.Predicate{}, false, err
	}
	if t.Desc {
		return graphql.After(f.Desc(), arg), true, nil
	}
	if t.NotNull {
		return graphql.After(f.Asc(), arg), true, nil
	}
	// ASC sorts NULLs last, so they fall after every non-null value.
	return graphql.Or(graphql.After(f.Asc(), arg), f.IsNull(true)), true, nil
}

// gqlEq renders an equality on a cursor value. Unlike the ordering comparisons —
// which graphql.After takes as any — equality is only exposed per column type,
// so the kind selects the typed field handle and the Go type its value takes.
func gqlEq(col string, kind Kind, arg any) (graphql.Predicate, error) {
	switch kind {
	case KindInt:
		v, ok := arg.(int64)
		if !ok {
			return graphql.Predicate{}, fmt.Errorf("%w: page token value for %q is not an integer", ErrInvalid, col)
		}
		return graphql.Int64Field{Col: col}.Eq(graphql.Int64(v)), nil
	case KindFloat:
		v, ok := arg.(float64)
		if !ok {
			return graphql.Predicate{}, fmt.Errorf("%w: page token value for %q is not a number", ErrInvalid, col)
		}
		return graphql.FloatField{Col: col}.Eq(v), nil
	case KindBool:
		v, ok := arg.(bool)
		if !ok {
			return graphql.Predicate{}, fmt.Errorf("%w: page token value for %q is not a boolean", ErrInvalid, col)
		}
		return graphql.BoolField{Col: col}.Eq(v), nil
	case KindDate, KindTimestamp:
		// Instants cross the wire in the same textual form they were parsed from,
		// which is what the GraphQL scalar takes.
		v, ok := arg.(time.Time)
		if !ok {
			return graphql.Predicate{}, fmt.Errorf("%w: page token value for %q is not a timestamp", ErrInvalid, col)
		}
		if kind == KindDate {
			return graphql.StringField{Col: col}.Eq(v.Format("2006-01-02")), nil
		}
		return graphql.StringField{Col: col}.Eq(v.Format(time.RFC3339Nano)), nil
	default:
		v, ok := arg.(string)
		if !ok {
			return graphql.Predicate{}, fmt.Errorf("%w: page token value for %q is not a string", ErrInvalid, col)
		}
		return graphql.StringField{Col: col}.Eq(v), nil
	}
}

// GraphQLColumn resolves the physical column a response field holds. Hasura names
// its fields in camelCase, which is the form the JSON tag carries.
func GraphQLColumn(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	return name
}

// List runs the paginated list through the engine's query handler: it builds
// one shared ListRequest from the spec-driven filter, order, and page bounds —
// ANDing in the scope predicates — fetches limit+1 rows, and mints the opaque
// next-page token. Invalid filter/order input is rejected with ErrInvalid
// before any query runs.
func (e *HasuraEngine[M]) List(ctx context.Context, in ListInput) ([]M, string, error) {
	terms, err := OrderTerms(e.spec, in.OrderBy)
	if err != nil {
		e.observer.Debug("rejected order_by", map[string]any{"table": e.spec.Table, "order_by": in.OrderBy, "error": err.Error()})
		return nil, "", err
	}
	order := gqlOrderTerms(terms)
	where, hasWhere, err := e.Predicate(in.Filter)
	if err != nil {
		e.observer.Debug("rejected filter", map[string]any{"table": e.spec.Table, "error": err.Error()})
		return nil, "", err
	}

	preds := make([]graphql.Predicate, 0, len(e.scope)+2)
	preds = append(preds, e.scope...)
	if hasWhere {
		preds = append(preds, where)
	}

	cursor, err := DecodeCursor(terms, in.PageToken)
	if err != nil {
		e.observer.Debug("rejected page_token", map[string]any{"table": e.spec.Table, "error": err.Error()})
		return nil, "", err
	}
	seek, hasSeek, err := keysetPredicate(terms, cursor)
	if err != nil {
		e.observer.Debug("rejected page_token", map[string]any{"table": e.spec.Table, "error": err.Error()})
		return nil, "", err
	}
	if hasSeek {
		preds = append(preds, seek)
	}

	limit := PageLimit(in)
	req := (&graphql.ListRequest{}).Limit(limit + 1)
	if len(order) > 0 {
		req = req.OrderBy(order...)
	}
	switch len(preds) {
	case 0:
	case 1:
		req = req.Where(preds[0])
	default:
		req = req.Where(graphql.And(preds...))
	}

	var rows []M
	err = e.observer.Span(ctx, "filterx.List/"+e.spec.Table, func(ctx context.Context) error {
		var er error
		rows, er = e.query.List(ctx, req)
		return er
	})
	if err != nil {
		return nil, "", err
	}
	return NextPage(rows, limit, terms, CamelCase, GraphQLColumn)
}

// gqlCondition translates one spec-listed condition.
func gqlCondition(spec Spec, c Condition) (graphql.Predicate, error) {
	if c.Field == "" {
		return gqlSearchPredicate(spec, c)
	}
	f, ok := spec.Fields[c.Field]
	if !ok {
		return graphql.Predicate{}, fmt.Errorf("%w: cannot filter by %q", ErrInvalid, c.Field)
	}
	col := CamelCase(f.Column)
	switch f.Kind {
	case KindText:
		sf := graphql.StringField{Col: col}
		switch c.Op {
		case OpEq:
			return sf.Eq(c.Value), nil
		case OpNeq:
			return sf.Neq(c.Value), nil
		case OpHas:
			return sf.ILike(ILikePattern(c.Value)), nil
		}
	case KindEnum:
		sf := graphql.StringField{Col: col}
		v := NormalizeEnum(f, c.Value)
		switch c.Op {
		case OpEq:
			return sf.Eq(v), nil
		case OpNeq:
			return sf.Neq(v), nil
		}
	case KindRef:
		sf := graphql.StringField{Col: col}
		switch c.Op {
		case OpEq:
			return sf.Eq(RefID(c.Value)), nil
		case OpNeq:
			return sf.Neq(RefID(c.Value)), nil
		}
	case KindDate:
		if _, err := ParseDate(c.Value); err != nil {
			return graphql.Predicate{}, err
		}
		// ISO dates compare lexicographically in chronological order, so the
		// string field's ordered operators are correct.
		if p, ok := gqlOrderedPred(graphql.StringField{Col: col}, c.Op, c.Value); ok {
			return p, nil
		}
	case KindTimestamp:
		if _, err := ParseTimestamp(c.Value); err != nil {
			return graphql.Predicate{}, err
		}
		if p, ok := gqlOrderedPred(graphql.StringField{Col: col}, c.Op, c.Value); ok {
			return p, nil
		}
	case KindInt:
		v, err := ParseInt(c.Value)
		if err != nil {
			return graphql.Predicate{}, err
		}
		nf := graphql.Int64Field{Col: col}
		switch c.Op {
		case OpEq:
			return nf.Eq(graphql.Int64(v)), nil
		case OpNeq:
			return nf.Neq(graphql.Int64(v)), nil
		case OpLte:
			return nf.Lte(graphql.Int64(v)), nil
		case OpGte:
			return nf.Gte(graphql.Int64(v)), nil
		}
	case KindFloat:
		v, err := ParseFloat(c.Value)
		if err != nil {
			return graphql.Predicate{}, err
		}
		nf := graphql.FloatField{Col: col}
		switch c.Op {
		case OpEq:
			return nf.Eq(v), nil
		case OpNeq:
			return nf.Neq(v), nil
		case OpLte:
			return nf.Lte(v), nil
		case OpGte:
			return nf.Gte(v), nil
		}
	case KindBool:
		v, err := ParseBool(c.Value)
		if err != nil {
			return graphql.Predicate{}, err
		}
		bf := graphql.BoolField{Col: col}
		switch c.Op {
		case OpEq:
			return bf.Eq(v), nil
		case OpNeq:
			return bf.Neq(v), nil
		}
	case KindTags:
		// Array containment has no BoolExp operator in the generated schema;
		// tags stay a gorm-only filter (register an Override to change that).
		return graphql.Predicate{}, fmt.Errorf("%w: cannot filter by %q here", ErrInvalid, c.Field)
	}
	return graphql.Predicate{}, fmt.Errorf("%w: unsupported operator for %q", ErrInvalid, c.Field)
}

// gqlSearchPredicate matches a bareword term against the spec's search columns.
func gqlSearchPredicate(spec Spec, c Condition) (graphql.Predicate, error) {
	if len(spec.Search) == 0 {
		return graphql.Predicate{}, fmt.Errorf("%w: free-text search is not supported here", ErrInvalid)
	}
	pat := ILikePattern(c.Value)
	preds := make([]graphql.Predicate, 0, len(spec.Search))
	for _, col := range spec.Search {
		preds = append(preds, graphql.StringField{Col: CamelCase(col)}.ILike(pat))
	}
	if len(preds) == 1 {
		return preds[0], nil
	}
	return graphql.Or(preds...), nil
}

// gqlOrderedPred maps an ordered-kind Op onto the string field's comparison.
func gqlOrderedPred(f graphql.StringField, op Op, v string) (graphql.Predicate, bool) {
	switch op {
	case OpEq:
		return f.Eq(v), true
	case OpLte:
		return f.Lte(v), true
	case OpGte:
		return f.Gte(v), true
	default:
		return graphql.Predicate{}, false
	}
}
