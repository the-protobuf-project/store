// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// oneof_case.go keeps a stored oneof discriminator (the "<oneof>_case" enum
// column) in step with whichever arm the proto carries. The discriminator is
// derived on every write from the proto alone, for scalar and message arms
// alike, so a duration arm records DURATION as surely as a value-object arm
// records END -- the value-object fragments used to set it only for their own
// arms, which left a scalar arm stored with no case at all.

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
)

// oneofCase is one real oneof of a message whose discriminator is stored.
type oneofCase struct {
	Column      *schema.Column
	OneofGoName string
	Arms        []oneofArm
}

// oneofArm is one member of a stored oneof.
type oneofArm struct {
	Wrapper string // qualified proto wrapper type, e.g. "availabilitypbv1.Availability_Duration"
	Label   string // stored discriminator value, e.g. "DURATION"
}

// oneofCases returns the stored-discriminator oneofs of msg on table t.
func oneofCases(pb *pbIndex, t *schema.Table, msg *protogen.Message) []oneofCase {
	var out []oneofCase
	for _, o := range msg.Oneofs {
		if o.Desc.IsSynthetic() {
			continue
		}
		cc := columnByName(t, string(o.Desc.Name())+"_case")
		if cc == nil || cc.Enum == nil {
			continue
		}
		oc := oneofCase{Column: cc, OneofGoName: o.GoName}
		for _, f := range o.Fields {
			oc.Arms = append(oc.Arms, oneofArm{
				Wrapper: pb.names.Of(string(f.GoIdent.GoImportPath)) + "." + f.GoIdent.GoName,
				Label:   naming.ScreamingSnake(string(f.Desc.Name())),
			})
		}
		out = append(out, oc)
	}
	return out
}

// oneofArmNames lists every member of o by proto field name.
func oneofArmNames(o *protogen.Oneof) []string {
	names := make([]string, 0, len(o.Fields))
	for _, f := range o.Fields {
		names = append(names, string(f.Desc.Name()))
	}
	return names
}

// touchedAny renders the mask condition that replaces a value-object group:
// any of its members touched -- or, for a oneof, any arm of it, scalar arms
// included, since switching to one must still clear the value-object row the
// old arm left behind.
func touchedAny(fields []string, c *voCase) string {
	if c != nil && len(c.Arms) > 0 {
		fields = c.Arms
	}
	conds := make([]string, 0, len(fields))
	for _, f := range fields {
		conds = append(conds, fmt.Sprintf("repox.GroupTouched(paths, %q)", f))
	}
	return strings.Join(conds, " || ")
}

// gqlCaseInput renders the client CreateInput discriminator assignment from
// the proto in src.
func gqlCaseInput(src string, c oneofCase) string {
	var b strings.Builder
	fmt.Fprintf(&b, "switch %s.Get%s().(type) {\n", src, c.OneofGoName)
	for _, a := range c.Arms {
		fmt.Fprintf(&b, "\tcase *%s:\n\t\tci.%s = %q\n", a.Wrapper, export(camel(c.Column.Name)), a.Label)
	}
	b.WriteString("\t}")
	return b.String()
}

// gqlCasePatch renders the UpdateInput discriminator from the merged proto:
// the set arm's label, or NULL when no arm is set.
func gqlCasePatch(c oneofCase) string {
	field := export(camel(c.Column.Name))
	var b strings.Builder
	fmt.Fprintf(&b, "switch merged.Get%s().(type) {\n", c.OneofGoName)
	for _, a := range c.Arms {
		fmt.Fprintf(&b, "\tcase *%s:\n\t\tpatch.%s = graphql.Value(%q)\n", a.Wrapper, field, a.Label)
	}
	fmt.Fprintf(&b, "\tdefault:\n\t\tpatch.%s = graphql.Null[string]()\n\t}", field)
	return b.String()
}
