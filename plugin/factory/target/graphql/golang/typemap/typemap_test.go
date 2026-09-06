// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package typemap

import (
	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"testing"

	"github.com/the-protobuf-project/protokit/graphql/ir"
)

// TestGoTypeElemNullability verifies that list element nullability is honored:
// [String!] -> []string but [String] -> []*string.
func TestGoTypeElemNullability(t *testing.T) {
	schema := &ir.Schema{Scalars: map[string]bool{"String": true}}
	m := New(schema, nil, dialect.Default())
	var q Qualifier

	cases := []struct {
		name string
		ft   ir.FieldType
		want string
	}{
		{"non-null elems", ir.FieldType{Base: "String", List: true, ElemNonNull: true}, "[]string"},
		{"nullable elems", ir.FieldType{Base: "String", List: true, ElemNonNull: false}, "[]*string"},
		{"nullable scalar", ir.FieldType{Base: "String"}, "*string"},
		{"non-null scalar", ir.FieldType{Base: "String", NonNull: true}, "string"},
	}
	for _, c := range cases {
		if got := m.GoType(c.ft, q); got != c.want {
			t.Errorf("%s: GoType = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestGoArgTypeNullableList verifies a nullable list arg becomes *[]T (with element
// nullability preserved) so go-graphql-client treats it as an optional list.
func TestGoArgTypeNullableList(t *testing.T) {
	schema := &ir.Schema{Scalars: map[string]bool{"String": true}}
	m := New(schema, nil, dialect.Default())
	var q Qualifier

	// nullable list, non-null elements: *[]string
	got := m.GoArgType(ir.FieldType{Base: "String", List: true, ElemNonNull: true}, q)
	if got != "*[]string" {
		t.Errorf("nullable list = %q, want *[]string", got)
	}
	// non-null list, nullable elements: []*string
	got = m.GoArgType(ir.FieldType{Base: "String", List: true, NonNull: true}, q)
	if got != "[]*string" {
		t.Errorf("non-null list nullable elems = %q, want []*string", got)
	}
}
