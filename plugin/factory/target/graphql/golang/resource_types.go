// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package golang

// resource_types.go classifies a resource's input types (bool_exp, order_by, insert, update) and renders CreateInput/UpdateInput.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/the-protobuf-project/protokit/graphql/ir"
	"github.com/the-protobuf-project/protokit/naming"
)

// isBoolExp reports whether base names a GraphQL boolean-expression input (it has the
// _and/_or/_not combinators). Both row filters and per-column comparisons qualify.
func (r *renderer) isBoolExp(base string) bool {
	in, ok := r.schema.Inputs[base]
	if !ok {
		return false
	}
	and, or, not := r.dialect.Combinators()
	for _, f := range in.Fields {
		if f.Name == and || f.Name == or || f.Name == not {
			return true
		}
	}
	return false
}

// isOrderBy reports whether base names an order-by input, distinguishing it from an
// insert-object list. Every field is either the sort-direction (a generated enum, or the
// runtime graphql.OrderBy when that enum is provided by the runtime) or a nested relation's
// order-by input (e.g. BounceResourceOrderByExp.campaignResource: CampaignResourceOrderByExp),
// which an insert object never is.
func (r *renderer) isOrderBy(base string) bool {
	return r.isOrderByExp(base, map[string]bool{})
}

// isOrderByExp is the recursive worker for isOrderBy; seen guards against cyclic relation
// order-by inputs (a relation that orders back through the originating type).
func (r *renderer) isOrderByExp(base string, seen map[string]bool) bool {
	in, ok := r.schema.Inputs[base]
	if !ok || len(in.Fields) == 0 {
		return false
	}
	if seen[base] {
		return true // already on the validation stack — accept to break the cycle
	}
	seen[base] = true
	for _, f := range in.Fields {
		switch {
		case r.mapper.IsEnum(f.Type.Base) || r.mapper.LeafGoType(f.Type.Base) == "graphql.OrderBy":
			// sort direction
		case !f.Type.List && r.mapper.IsInput(f.Type.Base) && r.isOrderByExp(f.Type.Base, seen):
			// nested relation order-by
		default:
			return false
		}
	}
	return true
}

// argByPredicate finds, across a resource's operations, the first arg whose type matches
// pred, returning its base type name (used to locate the BoolExp / insert / update types).
func (rg *resGen) findArg(pred func(ir.Arg) bool) (ir.Arg, bool) {
	for _, set := range [][]op{rg.queries, rg.mutations, rg.subs} {
		for _, o := range set {
			for _, a := range o.Op.Args {
				if pred(a) {
					return a, true
				}
			}
		}
	}
	return ir.Arg{}, false
}

// boolExpType returns the resource's row BoolExp type (a non-comparison BoolExp used as a
// where/check argument — i.e. one without an _eq field), or "" if absent.
func (r *renderer) boolExpType(rg *resGen) string {
	a, ok := rg.findArg(func(a ir.Arg) bool {
		return r.isBoolExp(a.Type.Base) && !r.hasField(a.Type.Base, r.comparisonMarker())
	})
	if !ok {
		return ""
	}
	return a.Type.Base
}

// containsBoolExp reports whether an input nests a BoolExp field (so it is a filter or
// aggregate wrapper, e.g. filter_input, rather than an update-columns patch).
func (r *renderer) containsBoolExp(base string) bool {
	in, ok := r.schema.Inputs[base]
	if !ok {
		return false
	}
	for _, f := range in.Fields {
		if r.isBoolExp(f.Type.Base) {
			return true
		}
	}
	return false
}

// insertObjectType returns the insert mutation's object-list element input type.
func (r *renderer) insertObjectType(rg *resGen) string {
	insert, ok := r.verbByFriendly("Create")
	if !ok {
		return ""
	}
	for _, o := range rg.mutations {
		if !strings.HasPrefix(o.Op.Name, insert.OpPrefix) {
			continue
		}
		for _, a := range o.Op.Args {
			if a.Type.List && r.mapper.IsInput(a.Type.Base) && !r.isOrderBy(a.Type.Base) {
				return a.Type.Base
			}
		}
	}
	return ""
}

// updateColumnsType returns the update mutation's update-columns input type.
func (r *renderer) updateColumnsType(rg *resGen) string {
	update, ok := r.verbByFriendly("Update")
	if !ok {
		return ""
	}
	for _, o := range rg.mutations {
		if !strings.HasPrefix(o.Op.Name, update.OpPrefix) {
			continue
		}
		for _, a := range o.Op.Args {
			if !a.Type.List && r.mapper.IsInput(a.Type.Base) && !r.isBoolExp(a.Type.Base) && !r.containsBoolExp(a.Type.Base) {
				return a.Type.Base
			}
		}
	}
	return ""
}

func (r *renderer) hasField(base, name string) bool {
	in, ok := r.schema.Inputs[base]
	if !ok {
		return false
	}
	for _, f := range in.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

// renderInputStruct renders CreateInput (from the insert object input) and UpdateInput
// (from the update_columns input, each column flattened to its set operand) for a resource.
func (r *renderer) renderInputStruct(name, doc string, fields []ir.Field, setOperand bool) string {
	if len(fields) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// %s\ntype %s struct {\n", doc, name)
	for _, f := range sortedFields(fields) {
		fdoc := naming.Doc(f.Description)
		if fdoc == "" {
			// Synthesized doc when the schema carries no description, so every
			// generated field is documented.
			if setOperand {
				fdoc = naming.Doc(export(f.Name) + " updates the " + strconv.Quote(f.Name) +
					" column: Value sets it, Null clears it, the zero Nullable leaves it unchanged.")
			} else if !f.Type.NonNull {
				fdoc = naming.Doc(export(f.Name) + " sets the " + strconv.Quote(f.Name) +
					" column; the zero value is omitted (column default / NULL).")
			} else {
				fdoc = naming.Doc(export(f.Name) + " sets the " + strconv.Quote(f.Name) + " column (required).")
			}
		}
		b.WriteString(fdoc)
		goType := r.mapper.GoParamType(f.Type, qResource)
		tag := f.Name
		if setOperand {
			// Update columns are three-state (unset/null/value) so a masked Update can clear a
			// column to null, not only the omit-or-set a plain omitzero field allows. SetColumns
			// reads the Nullable's instruction directly, so the json tag carries the column name
			// only (no omitzero).
			goType = "graphql.Nullable[" + r.columnSetType(f.Type.Base) + "]"
		} else if !f.Type.NonNull {
			tag += ",omitzero"
		}
		fmt.Fprintf(&b, "\t%s %s `json:%q`\n", export(f.Name), goType, tag)
	}
	b.WriteString("}\n")
	return b.String()
}

// columnSetType resolves an update-column input to its "set" operand Go type.
func (r *renderer) columnSetType(colBase string) string {
	in, ok := r.schema.Inputs[colBase]
	if !ok || len(in.Fields) == 0 {
		return "string"
	}
	for _, f := range in.Fields {
		for _, set := range r.dialect.SetOperands() {
			if f.Name == set {
				return r.mapper.GoParamType(f.Type, qResource)
			}
		}
	}
	return r.mapper.GoParamType(in.Fields[0].Type, qResource)
}
