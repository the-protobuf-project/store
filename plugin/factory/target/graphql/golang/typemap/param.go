// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package typemap

// param.go resolves IR field types to the native Go value types used for input fields and method parameters.

import "github.com/the-protobuf-project/protokit/graphql/ir"

// GoParamType returns the native Go type for an input/param field: lists become slices and
// everything else is the bare value type. Nullability is carried by the json:",omitzero"
// tag the renderer adds, not by the Go type, so callers never write pointers or wrappers.
func (m *Mapper) GoParamType(ft ir.FieldType, q Qualifier) string {
	if ft.List {
		return "[]" + m.elemType(ft, q)
	}
	return m.qualifiedLeaf(ft.Base, q)
}

// LeafGoType exposes the resolved Go type for a leaf base (scalar/enum), used by the
// generator to pick a predicate field-handle kind from a comparison operand.
func (m *Mapper) LeafGoType(base string) string { return m.leafGoType(base) }

// IsEnum reports whether base names a generated enum.
func (m *Mapper) IsEnum(base string) bool { return m.isEnum(base) }

// IsInput reports whether base names a generated input object.
func (m *Mapper) IsInput(base string) bool { return m.isInput(base) }
