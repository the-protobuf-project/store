// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package golang

// render_request.go classifies operation arguments (classify/argSpec) and renders the
// chained <Method>Request builders, mapping optional args onto shared runtime request
// types where they fit.

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/graphql/ir"
)

// argRole classifies how an operation argument surfaces in the generated Go API.
type argRole int

const (
	roleSkip      argRole = iota // not exposed (e.g. distinct_on, filter_input — deferred)
	roleScalar                   // required positional native scalar (id)
	roleCreate                   // required single CreateInput (insert objects)
	roleUpdate                   // required UpdateInput (update_columns)
	rolePredicate                // optional graphql.Predicate (where / pre_check / post_check)
	roleOrder                    // optional []graphql.OrderTerm (order_by)
	roleInt                      // optional int (limit / offset)
)

// argSpec is one operation argument resolved to its Go and GraphQL shapes. When parent is
// set, the spec is a field of a wrapper input (e.g. an aggregate filter_input) and is
// nested under parent in the variables map.
type argSpec struct {
	role       argRole
	goName     string // Go parameter / request-field name
	goType     string // Go type in the signature or request field
	gqlType    string // GraphQL variable type
	argName    string // GraphQL argument name (variables-map key)
	parent     string // wrapper arg name when nested, else ""
	parentType string // wrapper GraphQL input type when nested
}

func (s argSpec) required() bool {
	return s.role == roleScalar || s.role == roleCreate || s.role == roleUpdate
}

// classify resolves an operation's arguments to their specs, dropping deferred ones. A
// filter/aggregate wrapper input is flattened into nested child specs (where/limit/...).
func (r *renderer) classify(o op) []argSpec {
	var out []argSpec
	for _, a := range o.Op.Args {
		// A wrapper input nests a where BoolExp but is not itself one (e.g. an aggregate
		// filter_input): flatten it. The where BoolExp argument itself is left to specFor.
		if r.mapper.IsInput(a.Type.Base) && !a.Type.List && !r.isBoolExp(a.Type.Base) && r.containsBoolExp(a.Type.Base) {
			out = append(out, r.expandFilter(a)...)
			continue
		}
		if s := r.specFor(a); s.role != roleSkip {
			out = append(out, s)
		}
	}
	return out
}

// expandFilter flattens a wrapper input (e.g. filter_input) into its optional child specs,
// tagging each with the parent arg so argsBlock nests them into one variable.
func (r *renderer) expandFilter(a ir.Arg) []argSpec {
	in, ok := r.schema.Inputs[a.Type.Base]
	if !ok {
		return nil
	}
	var out []argSpec
	for _, f := range sortedFields(in.Fields) {
		s := r.specFor(ir.Arg{Name: f.Name, Type: f.Type})
		if s.role == roleSkip || s.required() {
			continue
		}
		s.parent = a.Name
		s.parentType = a.Type.Base
		out = append(out, s)
	}
	return out
}

func (r *renderer) specFor(a ir.Arg) argSpec {
	base := a.Type.Base
	switch {
	case r.mapper.IsInput(base) && a.Type.List && r.isOrderBy(base):
		return argSpec{role: roleOrder, goName: paramName(a.Name), goType: "[]graphql.OrderTerm", gqlType: listGQL(a), argName: a.Name}
	case r.mapper.IsInput(base) && a.Type.List:
		return argSpec{role: roleCreate, goName: "obj", goType: "CreateInput", gqlType: listGQL(a), argName: a.Name}
	case r.isBoolExp(base):
		return argSpec{role: rolePredicate, goName: paramName(a.Name), goType: "graphql.Predicate", gqlType: base, argName: a.Name}
	case r.mapper.IsInput(base) && r.containsBoolExp(base):
		// A filter/aggregate wrapper (it nests a where BoolExp), not an update patch.
		return argSpec{role: roleSkip}
	case r.mapper.IsInput(base):
		return argSpec{role: roleUpdate, goName: "patch", goType: "UpdateInput", gqlType: base, argName: a.Name}
	case a.Type.List:
		return argSpec{role: roleSkip}
	case a.Type.NonNull:
		return argSpec{role: roleScalar, goName: paramName(a.Name), goType: r.mapper.GoType(a.Type, qResource), gqlType: gqlType(a.Type), argName: a.Name}
	case r.isIntLeaf(base):
		return argSpec{role: roleInt, goName: paramName(a.Name), goType: "int", gqlType: gqlType(a.Type), argName: a.Name}
	default:
		return argSpec{role: roleSkip}
	}
}

func (r *renderer) isIntLeaf(base string) bool {
	switch r.mapper.LeafGoType(base) {
	case "int", "int32", "graphql.Int64":
		return true
	}
	return false
}

// listGQL renders the GraphQL list type for a list argument (e.g. "[InsertX!]").
func listGQL(a ir.Arg) string {
	elem := a.Type.Base
	if a.Type.ElemNonNull {
		elem += "!"
	}
	return "[" + elem + "]"
}

// optionalSpecs returns the specs that go into the request builder.
func optionalSpecs(specs []argSpec) []argSpec {
	var out []argSpec
	for _, s := range specs {
		if !s.required() {
			out = append(out, s)
		}
	}
	return out
}

// Optional-argument field-name sets the shared runtime request types cover. An operation whose
// optional arguments fit one of these maps onto that shared type (so its generated <Method>Request
// is a type alias and the handler satisfies the generic graphql.QueryHandler/MutationHandler).
var (
	listReqFields   = map[string]bool{"where": true, "orderBy": true, "limit": true, "offset": true}
	createReqFields = map[string]bool{"postCheck": true}
	updateReqFields = map[string]bool{"preCheck": true, "postCheck": true}
	deleteReqFields = map[string]bool{"preCheck": true}
)

// sharedRequestType returns the runtime graphql request type an operation's optional arguments
// map onto ("ListRequest"/"CreateRequest"/"UpdateRequest"/"DeleteRequest"), or "" when they do
// not fit a shared shape (then a resource-local request type is generated instead). Matching is
// by operation verb and a subset check, so an unconventional schema still generates compiling —
// if non-generic — code.
func (r *renderer) sharedRequestType(o op, specs []argSpec) string {
	names := map[string]bool{}
	for _, s := range optionalSpecs(specs) {
		names[s.goName] = true
	}
	fits := func(allowed map[string]bool) bool {
		for n := range names {
			if !allowed[n] {
				return false
			}
		}
		return true
	}
	hasVerb := func(friendly string) bool {
		v, ok := r.verbByFriendly(friendly)
		return ok && strings.HasPrefix(o.Op.Name, v.OpPrefix)
	}
	switch {
	case (o.Op.Kind == "query" || o.Op.Kind == "subscription") && fits(listReqFields):
		return "ListRequest"
	case o.Op.Kind == "mutation" && hasVerb("Create") && fits(createReqFields):
		return "CreateRequest"
	case o.Op.Kind == "mutation" && hasVerb("Update") && fits(updateReqFields):
		return "UpdateRequest"
	case o.Op.Kind == "mutation" && hasVerb("Delete") && fits(deleteReqFields):
		return "DeleteRequest"
	}
	return ""
}

// getterName is the accessor a handler calls to read an optional argument from its request
// (e.g. "where" -> "GetWhere"), matching the Get* methods on the shared and local request types.
func getterName(goName string) string { return "Get" + export(goName) }

// requestType renders the chained <Method>Request builder for an operation's optional
// arguments, or "" when it has none. When the arguments fit a shared runtime type the request is
// a type alias (so the handler satisfies the generic interfaces); otherwise a resource-local
// struct with the same builder/accessor surface is generated.
func (r *renderer) requestType(o op) string {
	// A FindOne op shares the sibling List request type, so it declares none of its own.
	if o.FindOne {
		return ""
	}
	specs := optionalSpecs(r.classify(o))
	if len(specs) == 0 {
		return ""
	}
	var b strings.Builder
	if shared := r.sharedRequestType(o, r.classify(o)); shared != "" {
		fmt.Fprintf(&b, "// %sRequest carries the optional arguments for %s.\ntype %sRequest = graphql.%s\n\n", o.Name, o.Name, o.Name, shared)
		fmt.Fprintf(&b, "// %s starts a builder for the optional arguments of %s.\nfunc %s() *%sRequest { return &%sRequest{} }\n\n", o.Name, o.Name, o.Name, o.Name, o.Name)
		return b.String()
	}
	fmt.Fprintf(&b, "// %sRequest carries the optional arguments for %s.\ntype %sRequest struct {\n", o.Name, o.Name, o.Name)
	for _, s := range specs {
		fmt.Fprintf(&b, "\t%s %s\n", s.goName, s.goType)
	}
	b.WriteString("}\n\n")
	fmt.Fprintf(&b, "// %s starts a builder for the optional arguments of %s.\nfunc %s() *%sRequest { return &%sRequest{} }\n\n", o.Name, o.Name, o.Name, o.Name, o.Name)
	for _, s := range specs {
		setter := export(s.argName)
		if s.role == roleOrder {
			fmt.Fprintf(&b, "// %s sets the result ordering.\nfunc (r *%sRequest) %s(v ...graphql.OrderTerm) *%sRequest { r.%s = v; return r }\n\n", setter, o.Name, setter, o.Name, s.goName)
		} else {
			fmt.Fprintf(&b, "// %s sets the %s argument.\nfunc (r *%sRequest) %s(v %s) *%sRequest { r.%s = v; return r }\n\n", setter, s.argName, o.Name, setter, s.goType, o.Name, s.goName)
		}
		fmt.Fprintf(&b, "func (r *%sRequest) %s() %s { return r.%s }\n\n", o.Name, getterName(s.goName), s.goType, s.goName)
	}
	return b.String()
}
