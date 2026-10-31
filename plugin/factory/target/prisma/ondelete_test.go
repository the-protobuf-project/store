// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package prisma

import "testing"

// A relation with no action refuses a delete whether it is required or
// optional: Prisma's own default for an optional one (SetNull) is never left
// to apply. An explicit action is written as it is.
func TestPrismaOnDelete(t *testing.T) {
	for _, c := range []struct {
		action   string
		optional bool
		want     string
	}{
		{"", false, ""},
		{"", true, "Restrict"},
		{"SET NULL", true, "SetNull"},
		{"CASCADE", false, "Cascade"},
		{"NO ACTION", true, "NoAction"},
	} {
		if got := prismaOnDelete(c.action, c.optional); got != c.want {
			t.Errorf("prismaOnDelete(%q, %v) = %q, want %q", c.action, c.optional, got, c.want)
		}
	}
}
