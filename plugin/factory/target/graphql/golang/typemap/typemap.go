// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package typemap maps GraphQL types to Go types for the generated client.
package typemap

import (
	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"github.com/the-protobuf-project/protokit/graphql/ir"
)

// Mapper resolves Go types for IR field types, honoring user scalar overrides.
type Mapper struct {
	schema    *ir.Schema
	overrides map[string]string
}

// New returns a Mapper for the schema. The dialect supplies the base scalar table
// (GraphQL scalar name -> Go type); overrides replaces or extends it.
func New(schema *ir.Schema, overrides map[string]string, d dialect.Dialect) *Mapper {
	base := d.DefaultScalars()
	merged := make(map[string]string, len(base)+len(overrides))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}
	return &Mapper{schema: schema, overrides: merged}
}

// UsesJSON reports whether base maps to a json.RawMessage Go type, so callers can
// decide whether to import encoding/json.
func (m *Mapper) UsesJSON(base string) bool {
	return m.overrides[base] == "json.RawMessage"
}

// leafGoType returns the Go type for a leaf base (scalar or enum). Known scalars use
// the mapping table; unknown scalars fall back to string (a safe default for opaque
// custom scalars). Enums and named types use their GraphQL name, which is a valid
// exported Go identifier and is itself generated.
func (m *Mapper) leafGoType(base string) string {
	if t, ok := m.overrides[base]; ok {
		return t
	}
	if m.schema.Scalars[base] {
		return "string"
	}
	return base
}

// Qualifier carries the package-qualifier prefixes for references to generated types
// from a given package (e.g. "models.", "inputs.", "enums."). A zero Qualifier leaves
// references unqualified (used within the package that defines the type).
type Qualifier struct {
	Models string
	Inputs string
	Enums  string
}

// GoType returns the Go type for a leaf or named-type field, applying list and
// nullability wrappers and qualifying references to generated types per q. Relations
// expanded inline by the selection renderer do not go through this function.
func (m *Mapper) GoType(ft ir.FieldType, q Qualifier) string {
	if ft.List {
		return "[]" + m.elemType(ft, q)
	}
	if !ft.NonNull {
		return "*" + m.qualifiedLeaf(ft.Base, q)
	}
	return m.qualifiedLeaf(ft.Base, q)
}

// elemType returns the list element Go type, adding a pointer when list elements are
// nullable (e.g. [String] -> []*string, [String!] -> []string).
func (m *Mapper) elemType(ft ir.FieldType, q Qualifier) string {
	elem := m.qualifiedLeaf(ft.Base, q)
	if !ft.ElemNonNull {
		elem = "*" + elem
	}
	return elem
}

// GoArgType returns the Go type for an operation argument (a GraphQL variable). It
// matches GoType except that a nullable list becomes *[]T: go-graphql-client infers a
// plain slice as a non-null list ([T!]!), so a pointer is needed to allow a nil
// (absent) nullable list argument.
func (m *Mapper) GoArgType(ft ir.FieldType, q Qualifier) string {
	if ft.List && !ft.NonNull {
		return "*[]" + m.elemType(ft, q)
	}
	return m.GoType(ft, q)
}

// qualifiedLeaf returns the leaf Go type, prefixed with the kind-appropriate qualifier
// when base names a generated object, input, or enum (built-ins/scalars are bare).
func (m *Mapper) qualifiedLeaf(base string, q Qualifier) string {
	t := m.leafGoType(base)
	switch {
	case m.isObject(base):
		return q.Models + t
	case m.isInput(base):
		return q.Inputs + t
	case m.isEnum(base):
		return q.Enums + t
	default:
		return t
	}
}

func (m *Mapper) isObject(base string) bool { _, ok := m.schema.Objects[base]; return ok }
func (m *Mapper) isInput(base string) bool  { _, ok := m.schema.Inputs[base]; return ok }
func (m *Mapper) isEnum(base string) bool   { _, ok := m.schema.Enums[base]; return ok }
