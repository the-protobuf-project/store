// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package subgraph

// strip.go removes the subgraph's prefixes from a scoped schema: from its
// root fields, its types, and every reference to them.

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/graphql/introspect"
)

// strip returns a deep copy of wire with the prefixes removed, and the Names
// that put them back.
func (s *scoper) strip(wire *introspect.Schema) (*introspect.Schema, *Names, error) {
	names := &Names{RootFieldPrefix: s.rootPrefix, TypeNamePrefix: s.typePrefix, Shared: map[string]bool{}}
	rename := map[string]string{}
	for _, t := range wire.Types {
		if s.ownsType(t.Name) {
			rename[t.Name] = strings.TrimPrefix(t.Name, s.typePrefix)
		} else if s.typePrefix != "" && !s.roots[t.Name] && !strings.HasPrefix(t.Name, "__") {
			names.Shared[t.Name] = true
		}
	}
	for wireName, bare := range rename {
		if names.Shared[bare] {
			return nil, nil, fmt.Errorf("graphql.type_name_prefix %q: %s strips to %s, which the subgraph also reaches unprefixed", s.typePrefix, wireName, bare)
		}
	}
	ref := func(r introspect.TypeRef) introspect.TypeRef { return renameRef(r, rename) }
	field := func(f introspect.Field) introspect.Field {
		f.Type = ref(f.Type)
		f.Args = renameValues(f.Args, rename)
		return f
	}

	out := &introspect.Schema{QueryType: wire.QueryType, MutationType: wire.MutationType, SubscriptionType: wire.SubscriptionType}
	for _, t := range wire.Types {
		if bare, ok := rename[t.Name]; ok {
			t.Name = bare
		}
		fields := make([]introspect.Field, 0, len(t.Fields))
		for _, f := range t.Fields {
			f = field(f)
			if s.roots[t.Name] && s.rootPrefix != "" {
				f.Name = strings.TrimPrefix(f.Name, s.rootPrefix)
			}
			fields = append(fields, f)
		}
		if t.Fields != nil {
			t.Fields = fields
		}
		t.InputFields = renameValues(t.InputFields, rename)
		if t.Interfaces != nil {
			ifaces := make([]introspect.TypeRef, 0, len(t.Interfaces))
			for _, i := range t.Interfaces {
				ifaces = append(ifaces, ref(i))
			}
			t.Interfaces = ifaces
		}
		out.Types = append(out.Types, t)
	}
	return out, names, nil
}

// renameValues copies vs with every type reference renamed.
func renameValues(vs []introspect.InputValue, rename map[string]string) []introspect.InputValue {
	if vs == nil {
		return nil
	}
	out := make([]introspect.InputValue, 0, len(vs))
	for _, v := range vs {
		v.Type = renameRef(v.Type, rename)
		out = append(out, v)
	}
	return out
}

// renameRef copies the reference chain r, renaming its named type.
func renameRef(r introspect.TypeRef, rename map[string]string) introspect.TypeRef {
	if bare, ok := rename[r.Name]; ok {
		r.Name = bare
	}
	if r.OfType != nil {
		inner := renameRef(*r.OfType, rename)
		r.OfType = &inner
	}
	return r
}
