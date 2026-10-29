// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package subgraph

// scope.go restricts an introspected supergraph to one subgraph's root fields
// and the types they reach, and strips the subgraph's prefixes from them.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/the-protobuf-project/protokit/graphql/introspect"
)

// Result is a schema scoped to one subgraph.
type Result struct {
	// Wire is the subgraph's part of the schema under its wire names: what
	// dump_schema persists, and what Scope accepts again with the same
	// prefixes.
	Wire *introspect.Schema
	// Bare is Wire with the prefixes stripped: the input to ir.Build.
	Bare *introspect.Schema
	// Names maps Bare's names back to Wire's; nil when no prefix is set.
	Names *Names
}

// Scope keeps the root fields and types of the subgraph whose names carry
// rootFieldPrefix and typeNamePrefix, plus every type they reach, and drops
// the rest of the schema (another subgraph's fields and types).
//
// A root field is the subgraph's when it carries rootFieldPrefix; with no
// root-field prefix, when its type or an argument's type carries
// typeNamePrefix. A type is kept when it carries typeNamePrefix or is reached
// from a kept root field or kept type; a reached type without the prefix
// (String, OrderBy) is shared and keeps its name on the wire.
//
// With both prefixes empty the schema is returned untouched and Names is nil.
// A prefix that matches nothing is an error, as is a stripped name that
// collides with a shared type the subgraph reaches.
func Scope(in *introspect.Schema, rootFieldPrefix, typeNamePrefix string) (*Result, error) {
	if rootFieldPrefix == "" && typeNamePrefix == "" {
		return &Result{Wire: in, Bare: in}, nil
	}
	s := scoper{in: in, rootPrefix: rootFieldPrefix, typePrefix: typeNamePrefix, roots: map[string]bool{}}
	for _, r := range []*introspect.TypeName{in.QueryType, in.MutationType, in.SubscriptionType} {
		if r != nil {
			s.roots[r.Name] = true
		}
	}
	if err := s.check(); err != nil {
		return nil, err
	}
	wire := s.filter()
	bare, names, err := s.strip(wire)
	if err != nil {
		return nil, err
	}
	return &Result{Wire: wire, Bare: bare, Names: names}, nil
}

// scoper carries one Scope call's inputs.
type scoper struct {
	in         *introspect.Schema
	rootPrefix string
	typePrefix string
	roots      map[string]bool // root operation type names (Query, Mutation, ...)
}

// ownsType reports whether name is a type the subgraph defines.
func (s *scoper) ownsType(name string) bool {
	return s.typePrefix != "" && !s.roots[name] && len(name) > len(s.typePrefix) && strings.HasPrefix(name, s.typePrefix)
}

// ownsField reports whether root field f is the subgraph's.
func (s *scoper) ownsField(f introspect.Field) bool {
	if strings.HasPrefix(f.Name, "__") {
		return false
	}
	if s.rootPrefix != "" {
		return len(f.Name) > len(s.rootPrefix) && strings.HasPrefix(f.Name, s.rootPrefix)
	}
	for _, n := range fieldRefs(f) {
		if s.ownsType(n) {
			return true
		}
	}
	return false
}

// check fails when a configured prefix matches nothing in the schema.
func (s *scoper) check() error {
	if s.typePrefix != "" {
		found := false
		for _, t := range s.in.Types {
			found = found || s.ownsType(t.Name)
		}
		if !found {
			return fmt.Errorf("graphql.type_name_prefix %q matches no type in the introspected schema (is it the subgraph's graphqlTypeNamePrefix?)", s.typePrefix)
		}
	}
	var seen []string
	dup := map[string]bool{}
	for _, t := range s.in.Types {
		if !s.roots[t.Name] {
			continue
		}
		for _, f := range t.Fields {
			if s.ownsField(f) {
				return nil
			}
			if !strings.HasPrefix(f.Name, "__") && !dup[f.Name] {
				dup[f.Name] = true
				seen = append(seen, f.Name)
			}
		}
	}
	sort.Strings(seen)
	if len(seen) > 5 {
		seen = append(seen[:5], "...")
	}
	if s.rootPrefix != "" {
		return fmt.Errorf("graphql.root_field_prefix %q matches no root field in the introspected schema (is it the subgraph's graphqlRootFieldPrefix? root fields seen: %s)", s.rootPrefix, strings.Join(seen, ", "))
	}
	return fmt.Errorf("graphql.type_name_prefix %q: no root field returns or takes a type carrying it (root fields seen: %s)", s.typePrefix, strings.Join(seen, ", "))
}

// filter returns the subgraph's part of the schema, under its wire names.
func (s *scoper) filter() *introspect.Schema {
	keep := map[string]bool{}
	var queue []string
	visit := func(names ...string) {
		for _, n := range names {
			if n != "" && !keep[n] && !s.roots[n] {
				keep[n] = true
				queue = append(queue, n)
			}
		}
	}
	out := &introspect.Schema{QueryType: s.in.QueryType, MutationType: s.in.MutationType, SubscriptionType: s.in.SubscriptionType}
	byName := map[string]*introspect.FullType{}
	for i := range s.in.Types {
		t := &s.in.Types[i]
		byName[t.Name] = t
		if s.ownsType(t.Name) {
			visit(t.Name)
		}
		if !s.roots[t.Name] {
			continue
		}
		for _, f := range t.Fields {
			if s.ownsField(f) {
				visit(fieldRefs(f)...)
			}
		}
	}
	for len(queue) > 0 {
		t := byName[queue[0]]
		queue = queue[1:]
		if t != nil {
			visit(typeRefs(t)...)
		}
	}
	for _, t := range s.in.Types {
		switch {
		case s.roots[t.Name]:
			root := t
			root.Fields = nil
			for _, f := range t.Fields {
				if s.ownsField(f) || strings.HasPrefix(f.Name, "__") {
					root.Fields = append(root.Fields, f)
				}
			}
			out.Types = append(out.Types, root)
		case keep[t.Name] || builtin[t.Name] || strings.HasPrefix(t.Name, "__"):
			out.Types = append(out.Types, t)
		}
	}
	return out
}

// builtin holds the GraphQL specification's scalars. Every schema has them
// and no subgraph prefixes them, so a scoped schema always keeps them.
var builtin = map[string]bool{"String": true, "Int": true, "Float": true, "Boolean": true, "ID": true}

// fieldRefs lists the named types field f returns and takes.
func fieldRefs(f introspect.Field) []string {
	out := []string{baseName(f.Type)}
	for _, a := range f.Args {
		out = append(out, baseName(a.Type))
	}
	return out
}

// typeRefs lists the named types t's fields, arguments and interfaces name.
func typeRefs(t *introspect.FullType) []string {
	var out []string
	for _, f := range t.Fields {
		out = append(out, fieldRefs(f)...)
	}
	for _, f := range t.InputFields {
		out = append(out, baseName(f.Type))
	}
	for _, i := range t.Interfaces {
		out = append(out, baseName(i))
	}
	return out
}

// baseName unwraps NON_NULL and LIST to the named type.
func baseName(r introspect.TypeRef) string {
	cur := &r
	for cur != nil && cur.Name == "" {
		cur = cur.OfType
	}
	if cur == nil {
		return ""
	}
	return cur.Name
}
