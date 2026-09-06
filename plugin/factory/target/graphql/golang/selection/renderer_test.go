// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package selection

import (
	"strings"
	"testing"

	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"github.com/the-protobuf-project/protokit/graphql/ir"
	"github.com/the-protobuf-project/store/plugin/factory/target/graphql/golang/typemap"
)

// A JSON scalar column decodes to an opaque json.RawMessage. The model struct
// field must carry a `scalar:"true"` tag so hasura/go-graphql-client's jsonutil
// decoder copies the raw JSON into the field instead of recursing into the object
// to match its keys against struct fields — which fails for a nullable
// (*json.RawMessage) field, producing:
//
//	struct field for "<key>" doesn't exist in any of 1 places to unmarshal
func TestModelBodyTagsJSONScalarFields(t *testing.T) {
	schema := &ir.Schema{
		Objects: map[string]*ir.Object{},
		Enums:   map[string]*ir.Enum{},
		Inputs:  map[string]*ir.Input{},
		Scalars: map[string]bool{"Json": true, "String1": true},
	}
	obj := &ir.Object{
		Name: "PropertyProperties",
		Fields: []ir.Field{
			// nullable jsonb -> *json.RawMessage
			{Name: "attributes", Type: ir.FieldType{Base: "Json"}},
			{Name: "displayName", Type: ir.FieldType{Base: "String1", NonNull: true}},
		},
	}
	schema.Objects[obj.Name] = obj

	r := New(schema, typemap.New(schema, nil, dialect.Default()), 5, typemap.Qualifier{})
	body := r.ModelBody(obj)

	wantAttr := "Attributes *json.RawMessage `graphql:\"attributes\" scalar:\"true\"`"
	if !strings.Contains(body, wantAttr) {
		t.Fatalf("JSON scalar field must be tagged scalar:\"true\"; got:\n%s", body)
	}

	wantName := "DisplayName string `graphql:\"displayName\"`"
	if !strings.Contains(body, wantName) {
		t.Fatalf("non-JSON field tag unexpected; got:\n%s", body)
	}
	if strings.Contains(body, "graphql:\"displayName\" scalar:\"true\"") {
		t.Fatalf("non-JSON field must not be tagged scalar; got:\n%s", body)
	}
}

// Two GraphQL fields can export to the same Go identifier: export() strips the
// leading underscore, so an aggregate's "_count" meta field and a column
// literally named "count" both become "Count", and the struct fails to compile
// with "Count redeclared".
//
// RFC 5545's recurrence rule has exactly this shape — COUNT is the number of
// occurrences — so every generated AggExp for that table was broken. The second
// field is suffixed instead; the GraphQL tag keeps the real name, so the wire
// query is unchanged.
func TestModelBodyDedupesCollidingGoNames(t *testing.T) {
	schema := &ir.Schema{
		Objects: map[string]*ir.Object{},
		Enums:   map[string]*ir.Enum{},
		Inputs:  map[string]*ir.Input{},
		Scalars: map[string]bool{"Int64": true},
	}
	obj := &ir.Object{
		Name: "EventRecurrencesAggExp",
		Fields: []ir.Field{
			{Name: "_count", Type: ir.FieldType{Base: "Int64", NonNull: true}},
			{Name: "count", Type: ir.FieldType{Base: "Int64"}},
		},
	}
	schema.Objects[obj.Name] = obj

	r := New(schema, typemap.New(schema, nil, dialect.Default()), 5, typemap.Qualifier{})
	body := r.ModelBody(obj)

	// The meta field is declared first, so it keeps the unsuffixed name — the
	// spelling existing consumers already depend on.
	if !strings.Contains(body, "Count graphql.Int64 `graphql:\"_count\"`") {
		t.Fatalf("_count must keep the Count name; got:\n%s", body)
	}
	if !strings.Contains(body, "Count2 *graphql.Int64 `graphql:\"count\"`") {
		t.Fatalf("the colliding column must be suffixed and keep its tag; got:\n%s", body)
	}
}
