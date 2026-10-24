// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package search_test

// boolean_test.go runs AIP-160's OR, NOT, `-` and parentheses through the
// projection search on Postgres -- Search, Count and Facet -- and, when
// STORE_TEST_OPENSEARCH_URL is set, through its OpenSearch copy, which must
// answer alike.

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/oh-tarnished/opensearch-go-sdk/opensearch"

	"github.com/the-protobuf-project/store/examples/search/gen/opensearch/osx"
	"github.com/the-protobuf-project/store/examples/search/gen/pb/searchdemov1"
	"github.com/the-protobuf-project/store/examples/search/gen/repository/repox"
	"github.com/the-protobuf-project/store/examples/search/gen/repository/search/search"
)

func TestBooleanFilters(t *testing.T) {
	ctx := context.Background()
	db := freshDB(t)
	var feeds []repox.ProjectionFeed
	var ix *struct {
		*osx.Index[*searchdemov1.RoomDay]
		c *opensearch.Client
	}
	if os.Getenv("STORE_TEST_OPENSEARCH_URL") != "" {
		i, c := testIndex(t)
		ix = &struct {
			*osx.Index[*searchdemov1.RoomDay]
			c *opensearch.Client
		}{i, c}
		feeds = append(feeds, i.Indexer(c, osx.IndexerOptions{Refresh: "true", OnError: func(_ context.Context, err error) { t.Error(err) }}))
	}
	f := newFixture(t, db, feeds...)

	// Room A: floor 1, meeting, 4 seats, sea view, gold. Room B: floor 2,
	// meeting and desk, 10 seats, silver. Hot desk C: floor 3, desk, 1 seat,
	// no badge. Three days each.
	for filter, want := range map[string]int{
		`floor = 1 OR floor = 3`:              6,
		`floor = 1 OR floor = 2 OR floor = 3`: 9,
		`NOT floor = 2`:                       6,
		`-floor = 2`:                          6,
		`-kinds:desk`:                         3,
		`NOT (floor = 1 OR floor = 2)`:        3,
		`(kinds:desk AND floor = 2) OR attributes.badge = "gold"`: 6,
		`kinds:meeting (floor = 2 OR attributes.seats < 5)`:       6,
		`kinds:meeting AND floor = 2 OR attributes.seats < 5`:     6, // OR binds tighter than AND
		`NOT attributes.badge = "gold"`:                           6, // C has no badge: it is not gold
		`-attributes.badge:*`:                                     3,
		`"Room A" OR desk`:                                        6,
		`-"Room"`:                                                 3,
		`NOT NOT floor = 1`:                                       3,
		`floor > 1 OR occupancy = FULL`:                           6,
		`-(floor = 1) -(floor = 3)`:                               3,
		`day = "2026-11-03" AND (occupancy = FULL OR floor = 3)`:  2,
	} {
		in := repox.SearchInput{Filter: filter, OrderBy: "display_name, day"}
		pg := search_(t, f.conn, in)
		if len(pg) != want {
			t.Errorf("Postgres Search(%s) = %d rows, want %d", filter, len(pg), want)
		}
		n, err := search.RoomDays.Count(ctx, f.conn, in)
		if err != nil || n != int64(want) {
			t.Errorf("Postgres Count(%s) = %d, %v; want %d", filter, n, err, want)
		}
		pgFacet, err := search.RoomDays.Facet(ctx, f.conn, in, "kinds", 0)
		if err != nil {
			t.Errorf("Postgres Facet(%s): %v", filter, err)
		}
		if ix == nil {
			continue
		}
		rows := osSearch(t, ix.Index, ix.c, in)
		if len(rows) != len(pg) {
			t.Errorf("OpenSearch Search(%s) = %d rows, Postgres %d", filter, len(rows), len(pg))
		} else {
			for i := range pg {
				if pg[i].GetRoom() != rows[i].GetRoom() || pg[i].GetDay().GetDay() != rows[i].GetDay().GetDay() {
					t.Errorf("%s: row %d differs: %v / %v", filter, i, pg[i], rows[i])
				}
			}
		}
		if n, err := ix.Count(ctx, ix.c, in); err != nil || n != int64(want) {
			t.Errorf("OpenSearch Count(%s) = %d, %v; want %d", filter, n, err, want)
		}
		osFacet, err := ix.Facet(ctx, ix.c, in, "kinds", 0)
		if err != nil || !slices.Equal(osFacet, pgFacet) {
			t.Errorf("Facet(%s): OpenSearch %v (%v), Postgres %v", filter, osFacet, err, pgFacet)
		}
	}
}
