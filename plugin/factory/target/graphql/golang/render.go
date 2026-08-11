// Package golang renders a Go client from the IR using an interface/handler
// architecture grouped by domain. Each resource is its own package exposing
// Query/Mutation/Subscription interfaces backed by unexported handlers, plus a natural
// calling surface: a predicate DSL (field handles + And/Or/Not), single-object
// CreateInput/UpdateInput structs, and chained request builders for optional arguments.
// A shared schema package holds models and a shared enums package holds enums; domain and
// root packages aggregate the handlers so callers write
// s.Query.<Domain>.<Resource>.<Method>(...).
package golang

import (
	"fmt"
	"sort"
	"strings"

	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"github.com/the-protobuf-project/protokit/graphql/ir"
	"github.com/the-protobuf-project/store/plugin/factory/target/graphql/golang/selection"
	"github.com/the-protobuf-project/store/plugin/factory/target/graphql/golang/typemap"
)

// Qualifiers for referencing generated types from each writing context. Models inline
// their relations, so they only reference enums; resource-package code references models
// bare — they are defined (or aliased) in the resource package itself by models.go — and
// enums as "enumsql." plus the runtime graphql helpers, which keep their names.
var (
	qModels   = typemap.Qualifier{Enums: "enumsql."}
	qResource = typemap.Qualifier{Models: "", Enums: "enumsql."}
)

// op pairs an operation with its de-duplicated exported method name. The list query also
// produces a second op (FindOne) that reuses the list operation but returns the first match
// as a single pointer; ListName carries the sibling List method whose request type it shares.
type op struct {
	Op       *ir.Operation
	Name     string
	FindOne  bool   // render a "first match" wrapper returning *Row instead of []Row
	ListName string // sibling List method (and request type) a FindOne op reuses
}

// requestName is the operation whose <Name>Request type this op uses. A FindOne op shares
// the sibling List request rather than declaring its own identical one.
func (o op) requestName() string {
	if o.FindOne {
		return o.ListName
	}
	return o.Name
}

// renderer turns IR elements into Go source fragments.
type renderer struct {
	schema    *ir.Schema
	mapper    *typemap.Mapper
	selection *selection.Renderer
	dialect   dialect.Dialect
}

// comparisonMarker is the field whose presence marks an input as a scalar
// comparison (rather than a row filter) — the dialect's canonical equality op.
func (r *renderer) comparisonMarker() string { return r.dialect.EqOperators()[0] }

// verbByFriendly returns the dialect verb whose friendly name matches (e.g.
// "Create"), reporting false when the dialect defines no such verb.
func (r *renderer) verbByFriendly(friendly string) (dialect.Verb, bool) {
	for _, v := range r.dialect.MutationVerbs() {
		if v.Friendly == friendly {
			return v, true
		}
	}
	return dialect.Verb{}, false
}

// enum renders a Go enum: a named string type plus a typed constant per value.
func (r *renderer) enum(e *ir.Enum) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// %s is the %s enum.\ntype %s string\n\n", e.Name, e.Name, e.Name)
	if len(e.Values) > 0 {
		b.WriteString("const (\n")
		for _, v := range e.Values {
			fmt.Fprintf(&b, "\t%s%s %s = %q\n", e.Name, export(v), e.Name, v)
		}
		b.WriteString(")\n")
	}
	return b.String()
}

// model renders an object's model struct, with relations inlined to max depth.
func (r *renderer) model(o *ir.Object) string {
	body := r.selection.ModelBody(o)
	var b strings.Builder
	fmt.Fprintf(&b, "// %s\ntype %s struct {\n%s}\n", modelDoc(o), o.Name, body)
	return b.String()
}

// modelDoc synthesizes the type's doc comment: the schema's description when
// present, otherwise wording derived from the object's role — rows, aggregate
// expressions, and the three mutation-response shapes each get a doc that says
// what the struct carries rather than restating its name.
func modelDoc(o *ir.Object) string {
	if d := strings.TrimSpace(o.Description); d != "" {
		return o.Name + " — " + strings.Split(d, "\n")[0]
	}
	name := o.Name
	switch {
	case strings.HasPrefix(name, "Insert") && strings.HasSuffix(name, "Response"):
		return name + " is the insert mutation's response: how many rows were\n// inserted, and the inserted rows as stored (server-set columns included)."
	case strings.HasPrefix(name, "Update") && strings.HasSuffix(name, "Response"):
		return name + " is the update mutation's response: how many rows matched\n// the key (0 = not found or a failed precondition), and the rows as updated."
	case strings.HasPrefix(name, "Delete") && strings.HasSuffix(name, "Response"):
		return name + " is the delete mutation's response: how many rows were\n// removed (0 = not found), and the removed rows' final values."
	case strings.HasSuffix(name, "AggExp"):
		return name + " aggregates the collection's rows: the row count plus\n// per-column counts, min/max, and numeric statistics."
	default:
		return name + " is one row of the collection, selected with every scalar\n// column; nullable columns are pointers so NULL stays distinguishable from a\n// zero value."
	}
}

// sortedKeys returns the keys of m in deterministic order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedFields returns input fields sorted by name for deterministic output.
func sortedFields(fields []ir.Field) []ir.Field {
	out := append([]ir.Field(nil), fields...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
