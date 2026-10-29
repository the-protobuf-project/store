// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package subgraph_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/the-protobuf-project/protokit/graphql/introspect"
	"github.com/the-protobuf-project/store/plugin/factory/subgraph"
)

// supergraph is a shared engine's schema as DDN composes it: aperio's
// subgraph (aperio_ / aperio_, one custom scalar), freebusy's with the same
// table under freebusy_ / Freebusy_, and a third subgraph with no prefixes at
// all. OrderBy and the built-in scalars are shared.
const supergraph = `
enum OrderBy { ASC DESC }
scalar aperio_Int32
scalar Freebusy_Int32
type aperio_EventResource { id: String! guests: aperio_Int32 }
input aperio_EventResourceBoolExp { _and: [aperio_EventResourceBoolExp!] id: String }
input aperio_EventResourceOrderByExp { id: OrderBy }
type Freebusy_EventResource { id: String! guests: Freebusy_Int32 }
input Freebusy_EventResourceBoolExp { id: String }
type EventResource { id: String! }
type Query {
  aperio_eventResource(where: aperio_EventResourceBoolExp, orderBy: [aperio_EventResourceOrderByExp!], limit: Int): [aperio_EventResource!]!
  freebusy_eventResource(where: Freebusy_EventResourceBoolExp): [Freebusy_EventResource!]!
  eventResource: [EventResource!]!
}
schema { query: Query }
`

func parse(t *testing.T, sdl string) *introspect.Schema {
	t.Helper()
	s, err := introspect.ParseSDL("supergraph.graphql", sdl)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func typeNames(s *introspect.Schema) []string {
	var out []string
	for _, t := range s.Types {
		if !strings.HasPrefix(t.Name, "__") {
			out = append(out, t.Name)
		}
	}
	sort.Strings(out)
	return out
}

func rootFields(s *introspect.Schema) []string {
	var out []string
	for _, t := range s.Types {
		if t.Name == "Query" {
			for _, f := range t.Fields {
				if !strings.HasPrefix(f.Name, "__") {
					out = append(out, f.Name)
				}
			}
		}
	}
	return out
}

func TestScopeKeepsOneSubgraph(t *testing.T) {
	in := parse(t, supergraph)
	r, err := subgraph.Scope(in, "aperio_", "aperio_")
	if err != nil {
		t.Fatal(err)
	}
	wantBare := []string{"Boolean", "EventResource", "EventResourceBoolExp", "EventResourceOrderByExp", "Float", "ID", "Int", "Int32", "OrderBy", "Query", "String"}
	if got := typeNames(r.Bare); !reflect.DeepEqual(got, wantBare) {
		t.Errorf("bare types = %v, want %v", got, wantBare)
	}
	if got := rootFields(r.Bare); !reflect.DeepEqual(got, []string{"eventResource"}) {
		t.Errorf("bare root fields = %v", got)
	}
	if got := rootFields(r.Wire); !reflect.DeepEqual(got, []string{"aperio_eventResource"}) {
		t.Errorf("wire root fields = %v", got)
	}
	for bare, wire := range map[string]string{
		"EventResourceBoolExp": "aperio_EventResourceBoolExp",
		"Int32":                "aperio_Int32",
		"Int":                  "Int",
		"String":               "String",
		"OrderBy":              "OrderBy",
	} {
		if got := r.Names.Type(bare); got != wire {
			t.Errorf("Type(%q) = %q, want %q", bare, got, wire)
		}
	}
	if got := r.Names.RootField("eventResource"); got != "aperio_eventResource" {
		t.Errorf("RootField = %q", got)
	}
	// References are renamed too: the row's guests column is the bare Int32.
	for _, ty := range r.Bare.Types {
		if ty.Name == "EventResource" && ty.Fields[1].Type.Name != "Int32" {
			t.Errorf("guests type = %q, want Int32", ty.Fields[1].Type.Name)
		}
	}
	// The input is not modified, and the wire schema scopes to itself.
	if len(rootFields(in)) != 3 {
		t.Error("Scope modified its input")
	}
	again, err := subgraph.Scope(r.Wire, "aperio_", "aperio_")
	if err != nil || !reflect.DeepEqual(typeNames(again.Bare), wantBare) {
		t.Errorf("re-scoping the wire schema: %v, %v", err, typeNames(again.Bare))
	}
}

func TestScopeUnprefixedIsIdentity(t *testing.T) {
	in := parse(t, supergraph)
	r, err := subgraph.Scope(in, "", "")
	if err != nil || r.Bare != in || r.Wire != in || r.Names != nil {
		t.Fatalf("want the schema untouched and nil names, got %v %v", r, err)
	}
	var n *subgraph.Names
	if n.Type("X") != "X" || n.RootField("x") != "x" {
		t.Error("nil Names must be the identity")
	}
}

// With only a type-name prefix, the subgraph's root fields are the ones that
// return or take its types.
func TestScopeTypePrefixOnly(t *testing.T) {
	r, err := subgraph.Scope(parse(t, supergraph), "", "Freebusy_")
	if err != nil {
		t.Fatal(err)
	}
	if got := rootFields(r.Bare); !reflect.DeepEqual(got, []string{"freebusy_eventResource"}) {
		t.Errorf("root fields = %v", got)
	}
	if r.Names.RootField("freebusy_eventResource") != "freebusy_eventResource" {
		t.Error("unstripped root fields keep their names")
	}
}

func TestScopeErrors(t *testing.T) {
	for _, c := range []struct {
		name, root, typ, sdl, want string
	}{
		{"root prefix matches nothing", "billing_", "", supergraph, `root_field_prefix "billing_" matches no root field`},
		{"type prefix matches nothing", "aperio_", "Billing_", supergraph, `type_name_prefix "Billing_" matches no type`},
		{"stripped name collides with a shared type", "a_", "A_", `
type A_Row { id: String! other: Row }
type Row { id: String! }
type Query { a_rows: [A_Row!]! }
`, "A_Row strips to Row"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := subgraph.Scope(parse(t, c.sdl), c.root, c.typ)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}
