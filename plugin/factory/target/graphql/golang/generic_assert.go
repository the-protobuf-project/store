// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package golang

// generic_assert.go emits the compile-time proofs that generated handlers satisfy the runtime graphql generic interfaces.

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/graphql/ir"
)

// genericAsserts renders the compile-time interface-satisfaction assertions for a resource: a
// `var _ graphql.QueryHandler[Row] = (*queryHandler)(nil)` (and the mutation equivalent) for
// each handler whose CRUD shape matches the generic runtime interface. They are the build-time
// proof that a generic adapter can drive this entity; a resource whose handlers do not fit the
// generic shape simply omits its assertion (and is not usable generically).
func (r *renderer) genericAsserts(rg *resGen) string {
	var b strings.Builder
	if a := r.queryAssert(rg); a != "" {
		b.WriteString("// Compile-time proof the query handler satisfies the generic graphql.QueryHandler.\n")
		b.WriteString(a)
		b.WriteByte('\n')
	}
	if a := r.mutationAssert(rg); a != "" {
		b.WriteString("// Compile-time proof the mutation handler satisfies the generic graphql.MutationHandler.\n")
		b.WriteString(a)
		b.WriteByte('\n')
	}
	return b.String()
}

// queryAssert renders the QueryHandler assertion when the resource has Get/List/Find methods
// matching the generic shape (a string-keyed Get, a ListRequest-driven List/Find over one row
// model), else "".
func (r *renderer) queryAssert(rg *resGen) string {
	var getOp, listOp, findOp *op
	for i := range rg.queries {
		o := &rg.queries[i]
		switch {
		case o.FindOne:
			findOp = o
		case o.Name == "List":
			listOp = o
		case o.Name == "Get":
			getOp = o
		}
	}
	if getOp == nil || listOp == nil || findOp == nil {
		return ""
	}
	if !r.getMatchesGeneric(*getOp) || !r.listMatchesGeneric(*listOp) {
		return ""
	}
	if getOp.Op.Return.Base != listOp.Op.Return.Base {
		return "" // Get and List must agree on the row model M
	}
	// The generic QueryHandler[M] takes the bare row model (Get returns *M, List returns []M),
	// so request the non-null form to avoid GoType's nullable pointer wrapper.
	model := r.mapper.GoType(ir.FieldType{Base: listOp.Op.Return.Base, NonNull: true}, qResource)
	return fmt.Sprintf("var _ graphql.QueryHandler[%s] = (*queryHandler)(nil)\n", model)
}

// mutationAssert renders the MutationHandler assertion when the resource has Create/Update/Delete
// methods matching the generic shape (CreateInput/UpdateInput bodies, a string key, the shared
// request variadics), else "".
func (r *renderer) mutationAssert(rg *resGen) string {
	var createOp, updateOp, deleteOp *op
	for i := range rg.mutations {
		o := &rg.mutations[i]
		switch o.Name {
		case "Create":
			createOp = o
		case "Update":
			updateOp = o
		case "Delete":
			deleteOp = o
		}
	}
	if createOp == nil || updateOp == nil || deleteOp == nil {
		return ""
	}
	if r.insertObjectType(rg) == "" || r.updateColumnsType(rg) == "" {
		return ""
	}
	if r.sharedRequestType(*createOp, r.classify(*createOp)) != "CreateRequest" ||
		r.sharedRequestType(*updateOp, r.classify(*updateOp)) != "UpdateRequest" ||
		r.sharedRequestType(*deleteOp, r.classify(*deleteOp)) != "DeleteRequest" {
		return ""
	}
	if !r.createMatchesGeneric(*createOp) || !r.updateMatchesGeneric(*updateOp) || !r.deleteMatchesGeneric(*deleteOp) {
		return ""
	}
	insertResp := r.mapper.GoType(createOp.Op.Return, qResource)
	updateResp := r.mapper.GoType(updateOp.Op.Return, qResource)
	deleteResp := r.mapper.GoType(deleteOp.Op.Return, qResource)
	return fmt.Sprintf("var _ graphql.MutationHandler[CreateInput, UpdateInput, %s, %s, %s] = (*mutationHandler)(nil)\n",
		insertResp, updateResp, deleteResp)
}

// getMatchesGeneric reports whether o is a by-id Get of the generic shape: exactly one string
// scalar key and no other arguments.
func (r *renderer) getMatchesGeneric(o op) bool {
	specs := r.classify(o)
	return len(specs) == 1 && specs[0].role == roleScalar && specs[0].goType == "string"
}

// listMatchesGeneric reports whether o is a List of the generic shape: only optional arguments,
// driven by the shared ListRequest (so the handler takes req ...*ListRequest).
func (r *renderer) listMatchesGeneric(o op) bool {
	specs := r.classify(o)
	if len(optionalSpecs(specs)) == 0 {
		return false
	}
	for _, s := range specs {
		if s.required() {
			return false
		}
	}
	return r.sharedRequestType(o, specs) == "ListRequest"
}

// createMatchesGeneric reports whether o takes a CreateInput body and no scalar key or patch.
func (r *renderer) createMatchesGeneric(o op) bool {
	hasCreate := false
	for _, s := range r.classify(o) {
		switch s.role {
		case roleCreate:
			hasCreate = true
		case roleScalar, roleUpdate:
			return false
		}
	}
	return hasCreate && len(optionalSpecs(r.classify(o))) > 0
}

// updateMatchesGeneric reports whether o takes one string key and an UpdateInput patch.
func (r *renderer) updateMatchesGeneric(o op) bool {
	scalars, updates := 0, 0
	strKey := false
	for _, s := range r.classify(o) {
		switch s.role {
		case roleScalar:
			scalars++
			strKey = s.goType == "string"
		case roleUpdate:
			updates++
		case roleCreate:
			return false
		}
	}
	return scalars == 1 && strKey && updates == 1 && len(optionalSpecs(r.classify(o))) > 0
}

// deleteMatchesGeneric reports whether o takes exactly one string key and no body.
func (r *renderer) deleteMatchesGeneric(o op) bool {
	scalars := 0
	strKey := false
	for _, s := range r.classify(o) {
		switch s.role {
		case roleScalar:
			scalars++
			strKey = s.goType == "string"
		case roleCreate, roleUpdate:
			return false
		}
	}
	return scalars == 1 && strKey && len(optionalSpecs(r.classify(o))) > 0
}
