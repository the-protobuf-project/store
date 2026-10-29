// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

import (
	"testing"

	"github.com/the-protobuf-project/protokit/schema"
)

// The DDN reference plugin matches the root fields of the requests the engine
// receives and sends a by-id query of its own, so on a prefixed subgraph every
// name in its config carries graphql_root_field_prefix; unprefixed, none does.
func TestDDNMutationsRootFieldPrefix(t *testing.T) {
	s := &schema.Schema{Name: "event"}
	rs := []*resource{{
		Table:   &schema.Table{Name: "resource"},
		Type:    "example.com/Event",
		Pattern: "events/{event}",
		ExtRefs: []extRef{{Field: "lead", Column: "lead", Type: "example.com/Lead", Policy: "ON_DELETE_ALLOW"}},
	}}
	for prefix, want := range map[string][4]string{
		"":        {"insertEventResource", "updateEventResourceById", "deleteEventResourceById", "eventResourceById"},
		"aperio_": {"aperio_insertEventResource", "aperio_updateEventResourceById", "aperio_deleteEventResourceById", "aperio_eventResourceById"},
	} {
		got := ddnMutations(s, rs, prefix)
		if len(got) != 3 {
			t.Fatalf("prefix %q: want 3 mutations, got %d", prefix, len(got))
		}
		if got[0].Field != want[0] || got[1].Field != want[1] || got[2].Field != want[2] || got[2].ByIDQuery != want[3] {
			t.Errorf("prefix %q: got %q %q %q (by id %q), want %q", prefix, got[0].Field, got[1].Field, got[2].Field, got[2].ByIDQuery, want)
		}
	}
}
