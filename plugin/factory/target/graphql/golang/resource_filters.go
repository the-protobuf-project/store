// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package golang

// resource_filters.go renders the predicate DSL: a field handle per filterable column plus And/Or/Not and relation filters.

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/graphql/ir"
	"github.com/the-protobuf-project/protokit/naming"
)

// renderPredicates renders predicates.go: a package-level field handle per filterable
// scalar column plus And/Or/Not. usesEnums reports whether the enums import is needed.
func (r *renderer) renderPredicates(rg *resGen) (body string, usesEnums bool) {
	base := r.boolExpType(rg)
	if base == "" {
		return "", false
	}
	// Field handles and relation filters share package scope with the And/Or/Not combinators,
	// the operation builders (List/Get/Find/Create/...), their <Method>Request types, the
	// CreateInput/UpdateInput structs, and the resource's model-alias types — so a column named
	// e.g. "list" or "listRequest" must not shadow any of them. Seed the used-set with those
	// reserved names; colliding handles get a deterministic numeric suffix.
	used := map[string]bool{"And": true, "Or": true, "Not": true, "CreateInput": true, "UpdateInput": true}
	for _, set := range [][]op{rg.queries, rg.mutations, rg.subs} {
		for _, o := range set {
			used[o.Name] = true
			used[o.Name+"Request"] = true
			if obj, ok := r.schema.Objects[o.Op.Return.Base]; ok {
				used[obj.Name] = true // model alias re-exported into this package
			}
		}
	}
	type fv struct{ name, expr string }
	type rel struct{ name, field string }
	var vars []fv
	var relations []rel
	and, or, not := r.dialect.Combinators()
	for _, f := range sortedFields(r.schema.Inputs[base].Fields) {
		if f.Name == and || f.Name == or || f.Name == not {
			continue
		}
		// Scalar comparisons have the comparison-marker field; relations are row
		// BoolExps without one.
		if r.hasField(f.Type.Base, r.comparisonMarker()) {
			expr, enum := r.fieldHandle(f)
			if enum {
				usesEnums = true
			}
			vars = append(vars, fv{naming.Unique(export(f.Name), used), expr})
			continue
		}
		if r.isBoolExp(f.Type.Base) {
			relations = append(relations, rel{naming.Unique(export(f.Name), used), f.Name})
		}
	}
	if len(vars) == 0 && len(relations) == 0 {
		return "", false
	}
	var b strings.Builder
	if len(vars) > 0 {
		fmt.Fprintf(&b, "// Filter fields for %s. Build predicates like %s.Eq(v) and combine\n// them with And/Or/Not.\nvar (\n", rg.res.Name, vars[0].name)
		for _, v := range vars {
			fmt.Fprintf(&b, "\t%s = %s\n", v.name, v.expr)
		}
		b.WriteString(")\n\n")
	}
	for _, rl := range relations {
		fmt.Fprintf(&b, "// %s filters by the %s relation, taking a predicate from that resource.\nfunc %s(p graphql.Predicate) graphql.Predicate { return graphql.Relation(%q, p) }\n\n", rl.name, rl.field, rl.name, rl.field)
	}
	b.WriteString("// And matches rows satisfying every predicate.\nfunc And(p ...graphql.Predicate) graphql.Predicate { return graphql.And(p...) }\n\n")
	b.WriteString("// Or matches rows satisfying any predicate.\nfunc Or(p ...graphql.Predicate) graphql.Predicate { return graphql.Or(p...) }\n\n")
	b.WriteString("// Not negates a predicate.\nfunc Not(p graphql.Predicate) graphql.Predicate { return graphql.Not(p) }\n")
	return b.String(), usesEnums
}

// fieldHandle returns the graphql field-handle expression for a filterable column and
// whether it references the enums package.
func (r *renderer) fieldHandle(f ir.Field) (string, bool) {
	operand := r.eqOperand(f.Type.Base)
	if operand != "" && r.mapper.IsEnum(operand) {
		return fmt.Sprintf("graphql.EnumField[enumsql.%s]{Col: %q}", operand, f.Name), true
	}
	kind := "graphql.StringField"
	switch r.mapper.LeafGoType(operand) {
	case "bool":
		kind = "graphql.BoolField"
	case "int", "int32", "graphql.Int64":
		kind = "graphql.Int64Field"
	case "float64":
		kind = "graphql.FloatField"
	case "json.RawMessage":
		kind = "graphql.JSONField"
	}
	return fmt.Sprintf("%s{Col: %q}", kind, f.Name), false
}

// eqOperand returns the operand type of a comparison input's _eq (or _in element).
func (r *renderer) eqOperand(cmp string) string {
	in, ok := r.schema.Inputs[cmp]
	if !ok {
		return ""
	}
	for _, f := range in.Fields {
		for _, op := range r.dialect.EqOperators() {
			if f.Name == op {
				return f.Type.Base
			}
		}
	}
	return ""
}
