// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package search_test

// jsonorder_test.go orders by a declared JSON attribute path
// ((store.v1.query).json_keys) -- on a projection Search and a resource List
// over Postgres, and on the OpenSearch copy -- paging by keyset with the key
// breaking ties; checks that a narrow read mask selects only what it needs;
// and that the Hasura engine refuses, plainly, what DDN cannot evaluate.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/the-protobuf-project/store/examples/search/gen/gorm/filterx"
	"github.com/the-protobuf-project/store/examples/search/gen/opensearch/osx"
	"github.com/the-protobuf-project/store/examples/search/gen/pb/searchdemov1"
	"github.com/the-protobuf-project/store/examples/search/gen/repository/repox"
	"github.com/the-protobuf-project/store/examples/search/gen/repository/search/search"
)

// seatsOf is each row's attributes.seats, in order.
func seatsOf(rows []*searchdemov1.RoomDay) []int {
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		out = append(out, int(r.GetAttributes().GetFields()["seats"].GetNumberValue()))
	}
	return out
}

// pageAll drains a search a page at a time, returning the rows and the
// number of pages.
func pageAll(t *testing.T, in repox.SearchInput, search func(repox.SearchInput) ([]*searchdemov1.RoomDay, string, error)) ([]*searchdemov1.RoomDay, int) {
	t.Helper()
	var all []*searchdemov1.RoomDay
	for pages := 1; ; pages++ {
		rows, next, err := search(in)
		if err != nil {
			t.Fatalf("Search(%+v): %v", in, err)
		}
		all = append(all, rows...)
		if next == "" {
			return all, pages
		}
		in.PageToken = next
	}
}

func TestProjectionOrdersByJSONPath(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, freshDB(t))
	run := func(in repox.SearchInput) ([]*searchdemov1.RoomDay, string, error) {
		return search.RoomDays.Search(ctx, f.conn, in)
	}

	// Seats: B 10, A 4, C 1; three days each. Pages of 2 cut through ties,
	// which the key breaks.
	all, pages := pageAll(t, repox.SearchInput{OrderBy: "attributes.seats desc, day", PageSize: 2}, run)
	if got, want := seatsOf(all), []int{10, 10, 10, 4, 4, 4, 1, 1, 1}; !slices.Equal(got, want) || pages != 5 {
		t.Errorf("seats desc = %v in %d pages, want %v in 5", got, pages, want)
	}
	for i := 1; i < len(all); i++ {
		if seatsOf(all[i-1 : i+1])[0] == seatsOf(all[i-1 : i+1])[1] && all[i-1].GetDay().GetDay() >= all[i].GetDay().GetDay() {
			t.Errorf("rows %d,%d: days out of order within equal seats", i-1, i)
		}
	}
	// Ascending, with the key alone breaking ties, under a filter.
	all, _ = pageAll(t, repox.SearchInput{Filter: "floor >= 1", OrderBy: "attributes.seats", PageSize: 4}, run)
	if got, want := seatsOf(all), []int{1, 1, 1, 4, 4, 4, 10, 10, 10}; !slices.Equal(got, want) {
		t.Errorf("seats asc = %v, want %v", got, want)
	}
	// Ordered by the path, read through a narrow mask: the page token still
	// carries the typed value.
	all, _ = pageAll(t, repox.SearchInput{OrderBy: "attributes.seats desc", PageSize: 2, ReadMask: []string{"floor"}}, run)
	if len(all) != 9 || all[0].GetFloor() != 2 || all[8].GetFloor() != 3 || all[0].GetAttributes() != nil || all[0].GetRoom() == "" {
		t.Errorf("seats desc with mask: %d rows, first %v, last %v", len(all), all[0], all[8])
	}

	// An undeclared path does not sort.
	_, _, err := run(repox.SearchInput{OrderBy: "attributes.badge"})
	if !errors.Is(err, repox.ErrInvalidArgument) || !strings.Contains(err.Error(), "json_keys") {
		t.Errorf("order by an undeclared path: err = %v, want ErrInvalidArgument naming json_keys", err)
	}
	// A token minted under one order does not resume another.
	_, next, err := run(repox.SearchInput{OrderBy: "attributes.seats", PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(repox.SearchInput{OrderBy: "floor", PageSize: 2, PageToken: next}); !errors.Is(err, repox.ErrInvalidArgument) {
		t.Errorf("token replayed under another order: err = %v", err)
	}
}

func TestResourceListOrdersByJSONPath(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, freshDB(t))
	in := repox.ListInput{OrderBy: "attributes.seats desc", PageSize: 1}
	var got []string
	for {
		rooms, next, err := f.repos.Rooms.List(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rooms {
			got = append(got, r.GetDisplayName())
		}
		if next == "" {
			break
		}
		in.PageToken = next
	}
	if want := []string{"Room B", "Room A", "Hot desk C"}; !slices.Equal(got, want) {
		t.Errorf("rooms by seats desc = %v, want %v", got, want)
	}
}

func TestProjectionSearchSelectsTheMask(t *testing.T) {
	db := freshDB(t)
	var (
		mu   sync.Mutex
		sqls []string
	)
	if err := db.Callback().Query().After("gorm:query").Register("test:capture", func(d *gorm.DB) {
		mu.Lock()
		defer mu.Unlock()
		sqls = append(sqls, d.Statement.SQL.String())
	}); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, db)
	last := func() string {
		mu.Lock()
		defer mu.Unlock()
		return sqls[len(sqls)-1]
	}

	rows := search_(t, f.conn, repox.SearchInput{Range: window(0, 10), ReadMask: []string{"room"}, OrderBy: "floor"})
	sql := last()
	selectList, _, _ := strings.Cut(sql, " FROM ")
	if len(rows) != 3 || rows[0].GetRoom() == "" || rows[0].GetDisplayName() != "" || rows[0].GetFree() != nil {
		t.Errorf("rows read through mask [room] = %v", rows)
	}
	if strings.Contains(selectList, "to_jsonb") || strings.Contains(selectList, `"free"`) || strings.Contains(selectList, `"attributes"`) {
		t.Errorf("mask [room] selects more than it needs: %s", selectList)
	}
	for _, col := range []string{`"room"`, `"day"`, `"floor"`} { // the mask, the key, the order
		if !strings.Contains(selectList, col) {
			t.Errorf("mask [room] ordered by floor does not select %s: %s", col, selectList)
		}
	}
	// No mask reads the whole row; a JSON path order adds its typed value.
	search_(t, f.conn, repox.SearchInput{OrderBy: "attributes.seats"})
	if selectList, _, _ := strings.Cut(last(), " FROM "); !strings.Contains(selectList, "to_jsonb") || !strings.Contains(selectList, "::bigint") {
		t.Errorf("no mask, ordered by attributes.seats: %s", selectList)
	}
}

func TestHasuraRefusesWhatDDNCannotEvaluate(t *testing.T) {
	eng := filterx.Hasura[map[string]any](search.RoomDays.Spec.Filter, nil)
	_, _, err := eng.Predicate([]filterx.Condition{{Field: "kinds", Op: filterx.OpHas, Value: "desk"}})
	if !errors.Is(err, filterx.ErrUnsupported) || !errors.Is(err, filterx.ErrInvalid) || !strings.Contains(err.Error(), "Postgres search connection") {
		t.Errorf("tag containment on Hasura: err = %v, want ErrUnsupported", err)
	}
	if err := repox.MapFilterxErr(err); !errors.Is(err, repox.ErrInvalidArgument) || !errors.Is(err, filterx.ErrUnsupported) {
		t.Errorf("MapFilterxErr = %v, want ErrInvalidArgument and ErrUnsupported", err)
	}
	if _, err := eng.OrderTerms("attributes.seats desc"); !errors.Is(err, filterx.ErrUnsupported) {
		t.Errorf("order by a JSON path on Hasura: err = %v, want ErrUnsupported", err)
	}
	if _, _, err := eng.Seek("attributes.seats", ""); !errors.Is(err, filterx.ErrUnsupported) {
		t.Errorf("seek by a JSON path on Hasura: err = %v, want ErrUnsupported", err)
	}
	// Plain orders and other filters are unaffected.
	if _, err := eng.OrderTerms("floor desc"); err != nil {
		t.Errorf("order by floor on Hasura: %v", err)
	}
	if _, _, err := eng.Predicate([]filterx.Condition{{Field: "floor", Op: filterx.OpGte, Value: "2"}}); err != nil {
		t.Errorf("floor >= 2 on Hasura: %v", err)
	}
}

func TestOpenSearchOrdersByJSONPath(t *testing.T) {
	ctx := context.Background()
	ix, c := testIndex(t)
	f := newFixture(t, freshDB(t), ix.Indexer(c, osx.IndexerOptions{Refresh: "true", OnError: func(_ context.Context, err error) { t.Error(err) }}))
	for _, in := range []repox.SearchInput{
		{OrderBy: "attributes.seats desc, day", PageSize: 2},
		{OrderBy: "attributes.seats", PageSize: 4},
		{OrderBy: "attributes.seats desc", PageSize: 2, ReadMask: []string{"floor"}},
	} {
		pg, _ := pageAll(t, in, func(in repox.SearchInput) ([]*searchdemov1.RoomDay, string, error) {
			return search.RoomDays.Search(ctx, f.conn, in)
		})
		os, pages := pageAll(t, in, func(in repox.SearchInput) ([]*searchdemov1.RoomDay, string, error) {
			return ix.Search(ctx, c, in)
		})
		if len(pg) != 9 || len(os) != 9 || pages < 3 {
			t.Fatalf("%q: Postgres %d rows, OpenSearch %d in %d pages", in.OrderBy, len(pg), len(os), pages)
		}
		for i := range pg {
			if pg[i].GetRoom() != os[i].GetRoom() || pg[i].GetDay().GetDay() != os[i].GetDay().GetDay() {
				t.Errorf("%q: row %d differs: Postgres %v, OpenSearch %v", in.OrderBy, i, pg[i], os[i])
			}
		}
	}
	if _, _, err := ix.Search(ctx, c, repox.SearchInput{OrderBy: "attributes.badge"}); !errors.Is(err, repox.ErrInvalidArgument) {
		t.Errorf("order by an undeclared path on OpenSearch: err = %v", err)
	}
}

func TestRangeTestFollowsScope(t *testing.T) {
	db := freshDB(t)
	var (
		mu   sync.Mutex
		sqls []string
	)
	if err := db.Callback().Query().After("gorm:query").Register("test:capture", func(d *gorm.DB) {
		mu.Lock()
		defer mu.Unlock()
		sqls = append(sqls, d.Statement.SQL.String())
	}); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, db)
	last := func() string {
		mu.Lock()
		defer mu.Unlock()
		return sqls[len(sqls)-1]
	}
	for d, h := range map[int]int{0: 9, 1: 12, 2: 15} {
		// Unscoped: the bare range test, which the GiST index serves.
		all := search_(t, f.conn, repox.SearchInput{Range: window(d, h), OrderBy: "display_name"})
		if strings.Contains(last(), "coalesce(") {
			t.Errorf("unscoped range search wraps the test: %s", last())
		}
		// Scoped by equality on a column leading a B-tree index (floor leads
		// floor, day, id): the coalesce form, and the same rows.
		for _, floor := range []string{"1", "2", "3"} {
			got := search_(t, f.conn, repox.SearchInput{Range: window(d, h), OrderBy: "display_name", Filter: "floor = " + floor})
			if !strings.Contains(last(), "coalesce(") {
				t.Errorf("range search scoped by floor = %s keeps the bare test: %s", floor, last())
			}
			want := slices.DeleteFunc(slices.Clone(all), func(r *searchdemov1.RoomDay) bool { return r.GetFloor() != int32(floor[0]-'0') })
			if !slices.Equal(names(got), names(want)) {
				t.Errorf("day %d %02d:00 floor %s = %v, want %v", d, h, floor, names(got), names(want))
			}
			n, err := search.RoomDays.Count(context.Background(), f.conn, repox.SearchInput{Range: window(d, h), Filter: "floor = " + floor})
			if err != nil || n != int64(len(want)) {
				t.Errorf("Count day %d floor %s = %d, %v; want %d", d, floor, n, err, len(want))
			}
		}
	}
	// A range filter (floor >= 2) is no equality: the bare test.
	search_(t, f.conn, repox.SearchInput{Range: window(0, 10), Filter: "floor >= 2"})
	if strings.Contains(last(), "coalesce(") {
		t.Errorf("range search with floor >= 2 wraps the test: %s", last())
	}
}
