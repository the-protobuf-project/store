// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package golang

// write_helpers.go writes the generated root field.go (runtime type re-exports).

// writeHelpers writes the root field.go, re-exporting the runtime scalar types and the
// Subscription type so generated clients read results and hold subscriptions without
// importing the runtime packages directly.
func (g *generator) writeHelpers() error {
	imports := []string{g.graphqlModule(), g.opts.RuntimeModule}
	return g.writeFile("", "field.go", g.opts.Package, imports, helpersBody)
}

// helpersBody is the body of the generated root field.go.
const helpersBody = `// Subscription is a live GraphQL subscription stream.
type Subscription = runtime.Subscription

// Int64 and Bigdecimal are precision-preserving scalar types used by model fields and
// input values; re-exported so reading results needs no extra import.
type (
	Int64      = graphql.Int64
	Bigdecimal = graphql.Bigdecimal
)

// Nullable is a three-state update-input value (unset / null / value); build one with
// Value, Null, or Unset. Re-exported so writing a masked Update needs no extra import.
type Nullable[T any] = graphql.Nullable[T]

// Value sets an update column to v; Null clears it to SQL NULL; Unset (the zero Nullable)
// leaves it unchanged.
func Value[T any](v T) Nullable[T] { return graphql.Value(v) }
func Null[T any]() Nullable[T]     { return graphql.Null[T]() }
func Unset[T any]() Nullable[T]    { return graphql.Unset[T]() }

// After builds a keyset (cursor) predicate selecting rows after the last one seen for an
// ordering, for stable pagination; a request's KeysetAfter applies it together with the order.
func After(term graphql.OrderTerm, last any) graphql.Predicate { return graphql.After(term, last) }

// Ptr returns a pointer to v, for the rare API that still needs one.
func Ptr[T any](v T) *T { return &v }
`
