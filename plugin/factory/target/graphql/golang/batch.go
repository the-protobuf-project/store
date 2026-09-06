// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package golang

// batch.go renders the batched-mutation surface: an <Method>Op per mutation plus Tx/Commit.

import (
	"fmt"
	"strings"
)

// Every mutation also gets an <Method>Op companion that, instead of executing immediately,
// returns a runtime.BatchOp describing the mutation and the result pointer it will fill. Callers
// queue several into a Tx and Commit them as one atomic GraphQL document (see runtime.Tx), so a
// write spanning tables commits together rather than fanning out across independent requests.

// opParams renders the parameter list for an <Method>Op: the same positional arguments as the
// immediate method (keys then body), then the result pointer, then the optional request
// variadic. It takes no context — the batch is executed later, by Tx.Commit.
func (r *renderer) opParams(o op) string {
	specs := r.classify(o)
	var parts []string
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
	parts = append(parts, "result *"+r.mapper.GoType(o.Op.Return, qResource))
	if len(optionalSpecs(usedSpecs(o, specs))) > 0 {
		parts = append(parts, "req ...*"+o.requestName()+"Request")
	}
	return strings.Join(parts, ", ")
}

// opSig renders the <Method>Op interface signature for a mutation op, or "" for non-mutations.
func (r *renderer) opSig(o op) string {
	if o.Op.Kind != "mutation" {
		return ""
	}
	return fmt.Sprintf("%sOp(%s) runtime.BatchOp", o.Name, r.opParams(o))
}

// opMethod renders the concrete <Method>Op handler method for a mutation op, or "" otherwise. It
// builds the same argument map as the immediate method but returns a deferred runtime.BatchOp.
func (r *renderer) opMethod(recv string, o op) string {
	if o.Op.Kind != "mutation" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// %sOp returns %s as a deferred mutation for a Tx batch; result is filled when the\n", o.Name, o.Name)
	fmt.Fprintf(&b, "// batch commits. Queue it with svc.Mutation.Tx().Add(...) to commit several mutations atomically.\n")
	fmt.Fprintf(&b, "func (h *%s) %sOp(%s) runtime.BatchOp {\n", recv, o.Name, r.opParams(o))
	b.WriteString(r.argsBlock(o))
	fmt.Fprintf(&b, "\treturn runtime.BatchOp{Field: %q, Args: args, Result: result}\n}\n", o.Op.Name)
	return b.String()
}
