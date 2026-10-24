// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package search_test

// opensearch_test.go runs the RoomDay projection's OpenSearch copy against
// the cluster STORE_TEST_OPENSEARCH_URL names (a throwaway one), fed by the
// Postgres repositories: the same searches answer alike on both, pages
// continue with search_after (on the live index, or through one point in
// time opened for the second page), facets come from terms aggregations,
// stale changes lose to external versions, and Reindex rebuilds the copy
// behind the alias.
//
//	STORE_TEST_OPENSEARCH_URL=http://localhost:19201 STORE_TEST_POSTGRES_DSN=... go test ./search/

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/oh-tarnished/opensearch-go-sdk/opensearch"

	"github.com/the-protobuf-project/store/examples/search/gen/gorm/filterx"
	"github.com/the-protobuf-project/store/examples/search/gen/opensearch/osx"
	"github.com/the-protobuf-project/store/examples/search/gen/opensearch/search/search"
	"github.com/the-protobuf-project/store/examples/search/gen/pb/searchdemov1"
	"github.com/the-protobuf-project/store/examples/search/gen/repository/repox"
)

// testIndex is RoomDayIndex under an alias of its own, dropped when the test
// ends.
func testIndex(t *testing.T) (*osx.Index[*searchdemov1.RoomDay], *opensearch.Client) {
	t.Helper()
	u := os.Getenv("STORE_TEST_OPENSEARCH_URL")
	if u == "" {
		t.Skip("STORE_TEST_OPENSEARCH_URL not set; skipping the OpenSearch tests")
	}
	c, err := opensearch.New(opensearch.Config{URL: u})
	if err != nil {
		t.Fatal(err)
	}
	ix := *search.RoomDayIndex
	ix.Spec.Alias = fmt.Sprintf("test_room_days_%d", time.Now().UnixNano())
	ctx := context.Background()
	if _, err := ix.Ensure(ctx, c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ix.Drop(context.Background(), c) })
	return &ix, c
}

func osSearch(t *testing.T, ix *osx.Index[*searchdemov1.RoomDay], c *opensearch.Client, in repox.SearchInput) []*searchdemov1.RoomDay {
	t.Helper()
	rows, _, err := ix.Search(context.Background(), c, in)
	if err != nil {
		t.Fatalf("OpenSearch Search(%+v): %v", in, err)
	}
	return rows
}

func TestOpenSearchMatchesPostgres(t *testing.T) {
	ctx := context.Background()
	ix, c := testIndex(t)
	f := newFixture(t, freshDB(t), ix.Indexer(c, osx.IndexerOptions{Refresh: "true", OnError: func(_ context.Context, err error) { t.Error(err) }}))
	if n, err := ix.Count(ctx, c, repox.SearchInput{}); err != nil || n != 9 {
		t.Fatalf("Count = %d, %v; want 9", n, err)
	}
	for _, in := range []repox.SearchInput{
		{Range: window(0, 9)},
		{Range: window(0, 10)},
		{Range: window(1, 12)},
		{Range: window(0, 19)},
		{Range: &repox.Span{Start: at(0, 8, 0), End: at(0, 9, 0)}},
		{Filter: `kinds:desk`},
		{Filter: `kinds:desk AND attributes.seats >= 5`},
		{Filter: `attributes.seats < 5`},
		{Filter: `attributes.view.sea = true`},
		{Filter: `attributes.view.sea:*`},
		{Filter: `attributes.badge != "gold"`},
		{Filter: `attributes.badge:*`},
		{Filter: `floor > 1 AND occupancy = FREE`},
		{Filter: `room`},
		{Filter: `day = "2026-11-03" AND occupancy = full`},
		{Range: window(2, 15), Filter: `kinds:meeting AND floor <= 2`},
		{Where: []filterx.Condition{filterx.Or(
			filterx.Condition{Field: "attributes.badge", Op: filterx.OpEq, Value: "gold", Quoted: true},
			filterx.Condition{Field: "attributes.seats", Op: filterx.OpEq, Value: "1"},
		)}},
	} {
		in.OrderBy = "display_name, day"
		pg := search_(t, f.conn, in)
		os := osSearch(t, ix, c, in)
		if len(pg) != len(os) {
			t.Errorf("%+v: Postgres %d rows, OpenSearch %d", in, len(pg), len(os))
			continue
		}
		for i := range pg {
			if pg[i].GetRoom() != os[i].GetRoom() || pg[i].GetDay().GetDay() != os[i].GetDay().GetDay() {
				t.Errorf("%+v: row %d differs: %v / %v", in, i, pg[i], os[i])
			}
		}
	}
	// A document decodes to the row Postgres holds.
	pg := search_(t, f.conn, repox.SearchInput{OrderBy: "floor, day"})
	os := osSearch(t, ix, c, repox.SearchInput{OrderBy: "floor, day"})
	for i := range pg {
		if fmt.Sprint(pg[i]) != fmt.Sprint(os[i]) {
			t.Errorf("row %d: Postgres %v, OpenSearch %v", i, pg[i], os[i])
		}
	}
	// An undeclared path cannot be compared in order on OpenSearch.
	if _, _, err := ix.Search(ctx, c, repox.SearchInput{Filter: `attributes.badge > "a"`}); !errors.Is(err, repox.ErrInvalidArgument) {
		t.Errorf("ordered compare of an undeclared path: err = %v, want ErrInvalidArgument", err)
	}
}

func TestOpenSearchPagesFacetsGet(t *testing.T) {
	ctx := context.Background()
	ix, c := testIndex(t)
	f := newFixture(t, freshDB(t), ix.Indexer(c, osx.IndexerOptions{Refresh: "true"}))

	// Pages of 4 over floor desc, day, on the live index: a write after the
	// first page sorting after it shows in a later one, and no row comes
	// twice. No point in time is opened.
	in := repox.SearchInput{OrderBy: "floor desc, day", PageSize: 4}
	var all []*searchdemov1.RoomDay
	for page := 0; ; page++ {
		rows, next, err := ix.Search(ctx, c, in)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, rows...)
		if page == 0 {
			mustRoom(t, f.repos, "Late room", 0, nil, map[string]any{})
		}
		if next == "" {
			break
		}
		in.PageToken = next
	}
	if len(all) != 12 || all[0].GetFloor() != 3 || all[11].GetFloor() != 0 || len(names(all)) != 4 {
		t.Errorf("paged %d rows over %d rooms; first floor %d", len(all), len(names(all)), all[0].GetFloor())
	}
	if n := openPITs(t); n != 0 {
		t.Errorf("live paging left %d points in time open", n)
	}

	// With PageKeepAlive, the pages after the first read one point in time,
	// opened for page 2: a write after it does not show in page 3, and the
	// last page deletes it.
	snap := *ix
	snap.PageKeepAlive = 30 * time.Second
	in = repox.SearchInput{OrderBy: "floor desc, day", PageSize: 4}
	all = nil
	for page := 0; ; page++ {
		rows, next, err := snap.Search(ctx, c, in)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, rows...)
		if want := min(page, 1); next != "" && openPITs(t) != want {
			t.Errorf("after page %d: %d points in time open, want %d", page+1, openPITs(t), want)
		}
		if page == 1 {
			mustRoom(t, f.repos, "Later room", -1, nil, map[string]any{})
		}
		if next == "" {
			break
		}
		in.PageToken = next
	}
	if len(all) != 12 || all[11].GetFloor() != 0 {
		t.Errorf("snapshot paged %d rows; last floor %d", len(all), all[len(all)-1].GetFloor())
	}
	if n := openPITs(t); n != 0 {
		t.Errorf("snapshot paging left %d points in time open", n)
	}
	if n, _ := ix.Count(ctx, c, repox.SearchInput{}); n != 15 {
		t.Errorf("Count after the late rooms = %d, want 15", n)
	}

	facets, err := ix.Facets(ctx, c, repox.SearchInput{Filter: "floor > 0"}, []string{"kinds", "occupancy", "attributes.badge", "attributes.seats", "day"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string][]repox.Bucket{
		"kinds":            {{Value: "desk", Count: 6}, {Value: "meeting", Count: 6}},
		"occupancy":        {{Value: "FREE", Count: 7}, {Value: "FULL", Count: 1}, {Value: "PARTIAL", Count: 1}},
		"attributes.badge": {{Value: "gold", Count: 3}, {Value: "silver", Count: 3}},
		"attributes.seats": {{Value: "1", Count: 3}, {Value: "4", Count: 3}, {Value: "10", Count: 3}},
		"day":              {{Value: "2026-11-02", Count: 3}, {Value: "2026-11-03", Count: 3}, {Value: "2026-11-04", Count: 3}},
	} {
		if !slices.Equal(facets[field], want) {
			t.Errorf("facet %s = %v, want %v", field, facets[field], want)
		}
	}
	pgFacet, err := f.roomDayFacet(ctx, "floor > 0", "attributes.badge")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pgFacet, facets["attributes.badge"]) {
		t.Errorf("Postgres facet %v != OpenSearch %v", pgFacet, facets["attributes.badge"])
	}

	// Get by key, narrowed by a read mask.
	key := &searchdemov1.RoomDay{Room: f.b.GetName(), Day: dayOf(f.b, days[1], nil).GetDay()}
	got, found, err := ix.Get(ctx, c, key, []string{"occupancy"})
	if err != nil || !found {
		t.Fatalf("Get = %v, %v", found, err)
	}
	if got.GetOccupancy() != searchdemov1.Occupancy_OCCUPANCY_FULL || got.GetRoom() != f.b.GetName() || got.GetDisplayName() != "" {
		t.Errorf("Get narrowed = %v", got)
	}
	masked := osSearch(t, ix, c, repox.SearchInput{Filter: "floor = 3", ReadMask: []string{"floor"}})
	if len(masked) != 3 || masked[0].GetFloor() != 3 || masked[0].GetKinds() != nil || masked[0].GetRoom() == "" {
		t.Errorf("search with read mask = %v", masked)
	}
	if _, _, err := ix.Search(ctx, c, repox.SearchInput{ReadMask: []string{"nope"}}); !errors.Is(err, repox.ErrInvalidArgument) {
		t.Errorf("bad read mask: err = %v", err)
	}
	// Iterate reads every match through a point in time.
	n := 0
	for m, err := range ix.Iterate(ctx, c, repox.SearchInput{Filter: "kinds:desk"}) {
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(m.GetKinds(), "desk") {
			t.Errorf("Iterate yielded %v", m)
		}
		n++
	}
	if n != 6 {
		t.Errorf("Iterate yielded %d, want 6", n)
	}
}

func TestOpenSearchVersionsAndReindex(t *testing.T) {
	ctx := context.Background()
	ix, c := testIndex(t)
	var stale []repox.Committed
	capture := repox.ProjectionFeedFunc(func(_ context.Context, cs []repox.Committed) {
		if stale == nil {
			stale = cs
		}
	})
	queued := ix.Indexer(c, osx.IndexerOptions{Queue: 16, Refresh: "true", OnError: func(_ context.Context, err error) { t.Error(err) }})
	f := newFixture(t, freshDB(t), capture, queued)

	// An update moves Room A to floor 7; replaying the first commit (A's
	// original rows, older versions) afterwards changes nothing.
	if _, err := f.repos.Rooms.Update(ctx, &searchdemov1.Room{Name: f.a.GetName(), Floor: 7}, []string{"floor"}); err != nil {
		t.Fatal(err)
	}
	if err := queued.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ix.Apply(ctx, c, stale, osx.ApplyOptions{Refresh: "true"}); err != nil {
		t.Fatalf("replaying a stale commit: %v", err)
	}
	if got := osSearch(t, ix, c, repox.SearchInput{Filter: "floor = 7"}); len(got) != 3 {
		t.Errorf("floor 7 after a stale replay = %d rows, want 3", len(got))
	}

	// Removals reach the index.
	sync := ix.Indexer(c, osx.IndexerOptions{Refresh: "true", OnError: func(_ context.Context, err error) { t.Error(err) }})
	f.conn.Projections.Feed = sync
	if err := f.repos.Rooms.Delete(ctx, f.c.GetName()); err != nil {
		t.Fatal(err)
	}
	if n, _ := ix.Count(ctx, c, repox.SearchInput{}); n != 6 {
		t.Errorf("Count after deleting C = %d, want 6", n)
	}

	// Drift: a document lost, a stray one added. Reindex rebuilds from the
	// table into a new index and swaps the alias.
	old, _ := c.AliasIndices(ctx, ix.Spec.Alias)
	id, _ := ix.Projection.Key(&searchdemov1.RoomDay{Room: f.a.GetName(), Day: dayOf(f.a, days[0], nil).GetDay()})
	if _, err := c.Delete(ctx, ix.Spec.Alias, id, &opensearch.WriteOptions{Refresh: "true"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Index(ctx, ix.Spec.Alias, `["rooms/stray","2026-11-02"]`, map[string]any{"id": `["rooms/stray","2026-11-02"]`, "room": "rooms/stray"}, &opensearch.WriteOptions{Refresh: "true"}); err != nil {
		t.Fatal(err)
	}
	res, err := ix.Reindex(ctx, c, ix.Source(ctx, f.conn), osx.ReindexOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Loaded != 6 || res.Pruned != 0 || !slices.Equal(res.Old, old) {
		t.Errorf("Reindex = %+v", res)
	}
	now, _ := c.AliasIndices(ctx, ix.Spec.Alias)
	if !slices.Equal(now, []string{res.Index}) {
		t.Errorf("alias points to %v, want %s", now, res.Index)
	}
	if exists, _ := c.IndexExists(ctx, old...); exists {
		t.Errorf("old index %v survived", old)
	}
	if n, _ := ix.Count(ctx, c, repox.SearchInput{}); n != 6 {
		t.Errorf("Count after Reindex = %d, want 6", n)
	}
	if got := osSearch(t, ix, c, repox.SearchInput{Filter: `room = "` + f.a.GetName() + `"`}); len(got) != 3 {
		t.Errorf("A's days after Reindex = %d, want 3", len(got))
	}
}

// openPITs is how many points in time the cluster holds open.
func openPITs(t *testing.T) int {
	t.Helper()
	resp, err := http.Get(strings.TrimRight(os.Getenv("STORE_TEST_OPENSEARCH_URL"), "/") + "/_search/point_in_time/_all")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Pits []json.RawMessage `json:"pits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return len(out.Pits)
}
