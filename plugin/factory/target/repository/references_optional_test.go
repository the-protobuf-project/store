// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

import (
	"testing"

	"github.com/the-protobuf-project/protokit/schema"
)

// An optional reference refuses a delete without a purge unless its field
// says an action that does not (SET NULL, CASCADE); a required one is left
// to its constraint.
func TestOptionalRef(t *testing.T) {
	tbl := &schema.Table{Name: "books", Columns: []*schema.Column{
		{Name: "genre", Optional: true},
		{Name: "former_genre", Optional: true},
		{Name: "shelf", NotNull: true},
	}}
	for _, c := range []struct {
		col, action string
		want        bool
	}{
		{"genre", "", true},
		{"genre", "NO ACTION", true},
		{"genre", "restrict", true},
		{"former_genre", "SET NULL", false},
		{"former_genre", "CASCADE", false},
		{"shelf", "", false},
	} {
		fk := &schema.ForeignKey{Column: c.col, OnDelete: c.action}
		if got := optionalRef(tbl, fk); got != c.want {
			t.Errorf("optionalRef(%s, %q) = %v, want %v", c.col, c.action, got, c.want)
		}
	}
	r := &resource{ReferencedBy: []referrerFK{{Join: true}}}
	if got := refusedBy(r); got != "ReferencedByJoin" {
		t.Errorf("refusedBy(joins only) = %s", got)
	}
	r.ReferencedBy = append(r.ReferencedBy, referrerFK{Optional: true})
	if got := refusedBy(r); got != "ReferencedByRow" {
		t.Errorf("refusedBy(with an optional reference) = %s", got)
	}
}
