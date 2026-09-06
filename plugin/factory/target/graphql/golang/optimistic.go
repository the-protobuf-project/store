// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package golang

// optimistic.go renders the optimistic-concurrency helpers (<Method>IfMatch), generated
// for update mutations that take a precondition filter and return an affected-rows count.

import (
	"fmt"
	"strings"
)

// An update mutation that accepts a preCheck row filter and returns an affected-rows count can
// offer first-class optimistic concurrency: run the update guarded by the precondition and treat
// "zero rows affected" as a conflict. qualifiesIfMatch reports whether op o is such an update.
func (r *renderer) qualifiesIfMatch(o op) bool {
	update, ok := r.verbByFriendly("Update")
	if o.FindOne || o.Op.Kind != "mutation" || !ok || !strings.HasPrefix(o.Op.Name, update.OpPrefix) {
		return false
	}
	return r.hasPreCheck(o) && r.affectedRowsField(o) != ""
}

// hasPreCheck reports whether o takes a preCheck row-filter argument (the guard predicate).
func (r *renderer) hasPreCheck(o op) bool {
	for _, a := range o.Op.Args {
		for _, name := range r.dialect.PreCheckArgs() {
			if a.Name == name && r.isBoolExp(a.Type.Base) {
				return true
			}
		}
	}
	return false
}

// affectedRowsField returns the exported Go name of o's response affected-rows count field, or
// "" if its response has none (so the helper can detect "no rows matched").
func (r *renderer) affectedRowsField(o op) string {
	obj, ok := r.schema.Objects[o.Op.Return.Base]
	if !ok {
		return ""
	}
	for _, f := range obj.Fields {
		for _, name := range r.dialect.AffectedRowsFields() {
			if f.Name == name {
				return export(f.Name)
			}
		}
	}
	return ""
}

// ifMatchParams renders the leading parameter list (the scalar keys then the patch) and the
// matching argument names for an UpdateIfMatch helper, or ok=false when o has no patch body.
func (r *renderer) ifMatchParams(o op) (params, argNames string, ok bool) {
	var parts, names []string
	for _, s := range r.classify(o) {
		if s.role == roleScalar {
			parts = append(parts, s.goName+" "+s.goType)
			names = append(names, s.goName)
		}
	}
	for _, s := range r.classify(o) {
		if s.role == roleUpdate {
			parts = append(parts, s.goName+" "+s.goType)
			names = append(names, s.goName)
			ok = true
		}
	}
	return strings.Join(parts, ", "), strings.Join(names, ", "), ok
}

// updateIfMatchSig renders the UpdateIfMatch interface method signature for o, or "" when o
// does not qualify.
func (r *renderer) updateIfMatchSig(o op) string {
	if !r.qualifiesIfMatch(o) {
		return ""
	}
	params, _, ok := r.ifMatchParams(o)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%sIfMatch(ctx context.Context, %s, match graphql.Predicate) (%s, error)",
		o.Name, params, r.mapper.GoType(o.Op.Return, qResource))
}

// updateIfMatchMethod renders the concrete UpdateIfMatch handler method for o, or "" when o
// does not qualify. It delegates to the plain Update with the precondition wired into preCheck
// and maps a zero affected-rows result to graphql.ErrConflict.
func (r *renderer) updateIfMatchMethod(recv string, o op) string {
	if !r.qualifiesIfMatch(o) {
		return ""
	}
	params, argNames, ok := r.ifMatchParams(o)
	if !ok {
		return ""
	}
	ret := r.mapper.GoType(o.Op.Return, qResource)
	var b strings.Builder
	fmt.Fprintf(&b, "// %sIfMatch runs %s guarded by match, an optimistic-concurrency precondition\n", o.Name, o.Name)
	fmt.Fprintf(&b, "// (e.g. Etag.Eq(prev)). It returns graphql.ErrConflict when no row matched the precondition.\n")
	fmt.Fprintf(&b, "func (h *%s) %sIfMatch(ctx context.Context, %s, match graphql.Predicate) (%s, error) {\n", recv, o.Name, params, ret)
	fmt.Fprintf(&b, "\tresp, err := h.%s(ctx, %s, %s().PreCheck(match))\n", o.Name, argNames, o.Name)
	b.WriteString("\tif err != nil {\n\t\treturn resp, err\n\t}\n")
	fmt.Fprintf(&b, "\tif resp.%s == 0 {\n\t\treturn resp, graphql.ErrConflict\n\t}\n", r.affectedRowsField(o))
	b.WriteString("\treturn resp, nil\n}\n")
	return b.String()
}
