// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package subgraph scopes a federated GraphQL schema to one subgraph by the
// prefixes Hasura DDN gives its names, and maps the bare names the generators
// work with back to the names on the wire.
//
// A DDN subgraph's subgraph.yaml can set definition.generator.
// graphqlRootFieldPrefix and graphqlTypeNamePrefix. The ddn CLI then writes
// every root field (queries, mutations, subscriptions) and every type the
// subgraph defines (objects, inputs, enums, the connector's scalars, bool,
// order-by and aggregate expressions, mutation responses) into its metadata as
// prefix + name, the prefix prepended verbatim with no change of case:
// eventResource becomes aperio_eventResource and EventResourceBoolExp becomes
// aperio_EventResourceBoolExp. Field names, argument names and enum values
// keep their names. What the subgraph does not define keeps its name too: the
// GraphQL built-in scalars (String, Int, Float, Boolean, ID), the root
// operation types, and the globals subgraph's OrderBy.
//
// Scope keeps one subgraph's root fields and types and strips its prefixes,
// so the IR, and every Go identifier derived from it, is exactly what an
// unprefixed engine yields. Names records what was stripped, so the GraphQL
// text a generator writes (root fields, variable types) carries the wire
// names again.
package subgraph

// Names maps the bare GraphQL names of a scoped schema back to their wire
// names. A nil *Names is the identity: no prefixes were configured.
type Names struct {
	// RootFieldPrefix is prepended to every root field on the wire; empty when
	// root fields were not stripped.
	RootFieldPrefix string
	// TypeNamePrefix is prepended to every type the subgraph defines; empty
	// when type names were not stripped.
	TypeNamePrefix string
	// Shared holds the types that kept their name on the wire: types the
	// subgraph reaches but does not define (built-in scalars, OrderBy).
	Shared map[string]bool
}

// RootField returns the wire name of root field bare.
func (n *Names) RootField(bare string) string {
	if n == nil {
		return bare
	}
	return n.RootFieldPrefix + bare
}

// Type returns the wire name of the named type bare.
func (n *Names) Type(bare string) string {
	if n == nil || n.TypeNamePrefix == "" || n.Shared[bare] || bare == "" {
		return bare
	}
	return n.TypeNamePrefix + bare
}
