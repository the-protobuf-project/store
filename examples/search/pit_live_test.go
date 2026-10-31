// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package search_test

// pit_live_test.go is the benchmark that exhausted OpenSearch's points in
// time, run against the cluster STORE_TEST_OPENSEARCH_URL names and the
// Postgres STORE_TEST_POSTGRES_DSN names: see opensearch_test.go.

import (
	"context"
	"testing"
	"time"

	"github.com/the-protobuf-project/store/examples/generated/go/opensearch/search/osx"
	"github.com/the-protobuf-project/store/examples/generated/go/protobuf/search/searchdemov1"
	"github.com/the-protobuf-project/store/examples/generated/go/repository/search/repox"
	"github.com/the-protobuf-project/store/examples/generated/go/repository/search/search/search"
)

// TestOpenSearchFirstPagesHoldNoPointInTime is the benchmark that exhausted
// search.max_open_pit_context (300): many first pages of searches that have
// more pages. They open no point in time now, and paging still matches
// Postgres page for page.
func TestOpenSearchFirstPagesHoldNoPointInTime(t *testing.T) {
	ctx := context.Background()
	ix, c := testIndex(t)
	f := newFixture(t, freshDB(t), ix.Indexer(c, osx.IndexerOptions{Refresh: "true", OnError: func(_ context.Context, err error) { t.Error(err) }}))
	before := openPITs(t)
	snap := *ix
	snap.PageKeepAlive = 30 * time.Second
	for i := range 1000 {
		x := ix
		if i%2 == 1 {
			x = &snap
		}
		rows, next, err := x.Search(ctx, c, repox.SearchInput{OrderBy: "floor desc", PageSize: 2})
		if err != nil {
			t.Fatalf("first page %d: %v", i, err)
		}
		if len(rows) != 2 || next == "" {
			t.Fatalf("first page %d: %d rows, next %q", i, len(rows), next)
		}
	}
	if n := openPITs(t); n != before {
		t.Errorf("1,000 first pages left %d points in time open (had %d)", n, before)
	}

	for _, in := range []repox.SearchInput{
		{OrderBy: "display_name, day", PageSize: 2},
		{OrderBy: "floor desc, day desc", PageSize: 4},
		{OrderBy: "attributes.seats desc", PageSize: 3},
		{Filter: "kinds:desk OR floor = 1", OrderBy: "day", PageSize: 2},
	} {
		pg, pgPages := pageAll(t, in, func(in repox.SearchInput) ([]*searchdemov1.RoomDay, string, error) {
			return search.RoomDays.Search(ctx, f.conn, in)
		})
		for _, x := range []*osx.Index[*searchdemov1.RoomDay]{ix, &snap} {
			got, pages := pageAll(t, in, func(in repox.SearchInput) ([]*searchdemov1.RoomDay, string, error) {
				return x.Search(ctx, c, in)
			})
			if len(got) != len(pg) || pages != pgPages {
				t.Errorf("%+v (keep-alive %v): OpenSearch %d rows in %d pages, Postgres %d in %d", in, x.PageKeepAlive, len(got), pages, len(pg), pgPages)
				continue
			}
			for i := range pg {
				if pg[i].GetRoom() != got[i].GetRoom() || pg[i].GetDay().GetDay() != got[i].GetDay().GetDay() {
					t.Errorf("%+v (keep-alive %v): row %d: OpenSearch %v, Postgres %v", in, x.PageKeepAlive, i, got[i], pg[i])
				}
			}
		}
	}
	if n := openPITs(t); n != before {
		t.Errorf("paging to the end left %d points in time open (had %d)", n, before)
	}
}
