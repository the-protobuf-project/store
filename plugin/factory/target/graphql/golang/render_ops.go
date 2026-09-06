// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package golang

// render_ops.go renders each resource's Query/Mutation/Subscription interfaces and their handlers.

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/graphql/ir"
	"github.com/the-protobuf-project/protokit/naming"
)

// iface renders an interface declaration with one method signature per op.
func (r *renderer) iface(name, doc string, ops []op) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// %s\ntype %s interface {\n", doc, name)
	for _, o := range ops {
		if o.FindOne {
			fmt.Fprintf(&b, "\t// %s runs the %q query and returns the first match, or nil if none.\n\t%s\n", o.Name, o.Op.Name, r.signature(o))
			continue
		}
		fmt.Fprintf(&b, "\t// %s runs the %q %s.\n\t%s\n", o.Name, o.Op.Name, o.Op.Kind, r.signature(o))
		if sig := r.updateIfMatchSig(o); sig != "" {
			fmt.Fprintf(&b, "\t// %sIfMatch runs %s guarded by an optimistic-concurrency precondition (e.g.\n\t// Etag.Eq(prev)), returning graphql.ErrConflict when no row matched.\n\t%s\n", o.Name, o.Name, sig)
		}
		if sig := r.opSig(o); sig != "" {
			fmt.Fprintf(&b, "\t// %sOp returns %s as a deferred mutation for atomic batching via a Tx.\n\t%s\n", o.Name, o.Name, sig)
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// handler renders the unexported handler struct plus a method per op.
func (r *renderer) handler(recv string, ops []op) string {
	var b strings.Builder
	fmt.Fprintf(&b, "type %s struct {\n\tgql *runtime.GraphQLClient\n}\n\n", recv)
	for _, o := range ops {
		b.WriteString(r.method(recv, o))
		b.WriteByte('\n')
		if extra := r.updateIfMatchMethod(recv, o); extra != "" {
			b.WriteString(extra)
			b.WriteByte('\n')
		}
		if extra := r.opMethod(recv, o); extra != "" {
			b.WriteString(extra)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// signature renders a method signature (no receiver): required args are positional;
// optional args collapse into a trailing variadic request builder.
func (r *renderer) signature(o op) string {
	specs := r.classify(o)
	parts := []string{"ctx context.Context"}
	// Scalar keys (e.g. id) come before the object/patch body for natural call order.
	for _, s := range specs {
		if s.role == roleScalar {
			parts = append(parts, s.goName+" "+s.goType)
		}
	}
	for _, s := range specs {
		if s.role == roleCreate || s.role == roleUpdate {
			parts = append(parts, s.goName+" "+s.goType)
		}
	}
	if len(optionalSpecs(usedSpecs(o, specs))) > 0 {
		parts = append(parts, "req ...*"+o.requestName()+"Request")
	}
	join := strings.Join(parts, ", ")
	switch {
	case o.Op.Kind == "subscription":
		return fmt.Sprintf("%s(%s) (*runtime.Subscription, error)", o.Name, join)
	case o.FindOne:
		return fmt.Sprintf("%s(%s) (%s, error)", o.Name, join, r.findOneReturn(o))
	default:
		return fmt.Sprintf("%s(%s) (%s, error)", o.Name, join, r.mapper.GoType(o.Op.Return, qResource))
	}
}

// findOneReturn is the FindOne return type: a pointer to the list query's element row
// (e.g. []BounceResource -> *BounceResource).
func (r *renderer) findOneReturn(o op) string {
	return r.mapper.GoType(ir.FieldType{Base: o.Op.Return.Base}, qResource)
}

// method renders the concrete handler method implementing op.
func (r *renderer) method(recv string, o op) string {
	if o.FindOne {
		return r.findOneMethod(recv, o)
	}
	retGo := r.mapper.GoType(o.Op.Return, qResource)
	var b strings.Builder
	fmt.Fprintf(&b, "func (h *%s) %s {\n", recv, r.signature(o))
	fmt.Fprintf(&b, "\tvar out %s\n", retGo)
	b.WriteString(r.argsBlock(o))
	switch o.Op.Kind {
	case "subscription":
		fmt.Fprintf(&b, "\treturn h.gql.SubscribeFields(ctx, %q, &out, args)\n}\n", o.Op.Name)
		return b.String()
	case "mutation":
		fmt.Fprintf(&b, "\tres := <-h.gql.MutateFields(ctx, %q, &out, args)\n", o.Op.Name)
	default:
		fmt.Fprintf(&b, "\tres := <-h.gql.QueryFields(ctx, %q, &out, args)\n", o.Op.Name)
	}
	b.WriteString("\treturn out, res.Error\n}\n")
	return b.String()
}

// findOneMethod renders the Find companion: it runs the list query (forcing limit 1 when the
// query supports it) and returns the first row, or nil when none match.
func (r *renderer) findOneMethod(recv string, o op) string {
	listGo := r.mapper.GoType(o.Op.Return, qResource) // []Row
	var b strings.Builder
	fmt.Fprintf(&b, "func (h *%s) %s {\n", recv, r.signature(o))
	fmt.Fprintf(&b, "\tvar out %s\n", listGo)
	b.WriteString(r.argsBlock(o))
	if lt := limitGQLType(r.classify(o)); lt != "" {
		fmt.Fprintf(&b, "\targs[\"limit\"] = graphql.VarPtr(1, %q)\n", lt)
	}
	fmt.Fprintf(&b, "\tres := <-h.gql.QueryFields(ctx, %q, &out, args)\n", o.Op.Name)
	b.WriteString("\tif res.Error != nil {\n\t\treturn nil, res.Error\n\t}\n")
	b.WriteString("\tif len(out) == 0 {\n\t\treturn nil, nil\n\t}\n")
	b.WriteString("\treturn &out[0], nil\n}\n")
	return b.String()
}

// usedSpecs returns the specs argsBlock actually renders for o. A FindOne op hard-codes
// limit to 1, so it drops the limit spec — keeping the signature's request parameter and the
// body's local request variable in agreement about whether either is needed.
func usedSpecs(o op, specs []argSpec) []argSpec {
	if !o.FindOne {
		return specs
	}
	out := make([]argSpec, 0, len(specs))
	for _, s := range specs {
		if s.argName == "limit" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// limitGQLType returns the GraphQL variable type of the operation's "limit" argument, or ""
// when it has none. The Find companion uses it so its forced limit=1 variable is declared with
// the same type the List method derives (e.g. Int, Int32), not a hard-coded "Int".
func limitGQLType(specs []argSpec) string {
	for _, s := range specs {
		if s.argName == "limit" {
			return s.gqlType
		}
	}
	return ""
}

// argsBlock renders the variables-map construction. Required args are set unconditionally
// (objects wrapped into a one-element slice, an update patch flattened to set-columns);
// optional args are added only when non-zero, so the runtime omits them entirely.
func (r *renderer) argsBlock(o op) string {
	specs := r.classify(o)
	var b strings.Builder
	if len(optionalSpecs(usedSpecs(o, specs))) > 0 {
		fmt.Fprintf(&b, "\tvar r %sRequest\n\tif len(req) > 0 && req[0] != nil {\n\t\tr = *req[0]\n\t}\n", o.requestName())
	}
	b.WriteString("\targs := map[string]any{}\n")
	for _, s := range specs {
		if s.parent != "" {
			continue
		}
		// Find forces limit to 1 after this block, so it omits the caller's limit entirely.
		if o.FindOne && s.argName == "limit" {
			continue
		}
		switch s.role {
		case roleScalar:
			fmt.Fprintf(&b, "\targs[%q] = graphql.Var(%s, %q)\n", s.argName, s.goName, s.gqlType)
		case roleCreate:
			fmt.Fprintf(&b, "\targs[%q] = graphql.Var([]CreateInput{%s}, %q)\n", s.argName, s.goName, s.gqlType)
		case roleUpdate:
			fmt.Fprintf(&b, "\targs[%q] = graphql.Var(graphql.SetColumns(%s), %q)\n", s.argName, s.goName, s.gqlType)
		default: // rolePredicate, roleOrder, roleInt
			// Optional arguments live on a shared request type in another package, so read them
			// through the Get* accessor rather than the unexported field.
			g := getterName(s.goName)
			fmt.Fprintf(&b, "\tif !graphql.IsOmitted(r.%s()) {\n\t\targs[%q] = graphql.VarPtr(r.%s(), %q)\n\t}\n", g, s.argName, g, s.gqlType)
		}
	}
	b.WriteString(r.nestedArgs(specs))
	return b.String()
}

// nestedArgs renders the construction of wrapper variables (e.g. an aggregate
// filter_input) from their flattened child specs, including each child only when set.
func (r *renderer) nestedArgs(specs []argSpec) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, p := range specs {
		if p.parent == "" || seen[p.parent] {
			continue
		}
		seen[p.parent] = true
		local := paramName(p.parent)
		fmt.Fprintf(&b, "\t%s := map[string]any{}\n", local)
		for _, c := range specs {
			if c.parent != p.parent {
				continue
			}
			g := getterName(c.goName)
			fmt.Fprintf(&b, "\tif !graphql.IsOmitted(r.%s()) {\n\t\t%s[%q] = r.%s()\n\t}\n", g, local, c.argName, g)
		}
		fmt.Fprintf(&b, "\tif len(%s) > 0 {\n\t\targs[%q] = graphql.VarPtr(%s, %q)\n\t}\n", local, p.parent, local, p.parentType)
	}
	return b.String()
}

// gqlType renders an argument's GraphQL type WITHOUT the outer non-null "!" (which
// go-graphql-client appends for non-pointer Var values and omits for VarPtr).
func gqlType(ft ir.FieldType) string {
	if ft.List {
		elem := ft.Base
		if ft.ElemNonNull {
			elem += "!"
		}
		return "[" + elem + "]"
	}
	return ft.Base
}

// paramName converts a GraphQL argument name to a lowerCamel Go parameter.
func paramName(name string) string {
	parts := strings.Split(strings.TrimLeft(name, "_"), "_")
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	out := strings.Join(parts, "")
	if out == "" {
		return "arg"
	}
	if naming.GoKeyword(out) {
		out += "_"
	}
	return out
}
