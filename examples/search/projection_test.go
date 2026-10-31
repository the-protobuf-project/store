// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package search_test

// projection_test.go runs the RoomDay projection on Postgres: kept current by
// repository writes and Batch commits in their transactions, searched by
// time window, AIP-160 filter (tags, JSON attribute paths, free text),
// order_by and keyset pages, counted, faceted, rebuilt, and fed to a
// ProjectionFeed after each commit.

import (
	"context"
	"errors"
	"iter"
	"slices"
	"testing"

	"github.com/the-protobuf-project/store/examples/generated/go/gorm/search/filterx"
	"github.com/the-protobuf-project/store/examples/generated/go/protobuf/search/searchdemov1"
	"github.com/the-protobuf-project/store/examples/generated/go/repository/search/repox"
	"github.com/the-protobuf-project/store/examples/generated/go/repository/search/search/search"
)

// window is an hour on day d starting at hour h.
func window(d, h int) *repox.Span {
	return &repox.Span{Start: at(d, h, 0), End: at(d, h+1, 0)}
}

// names are the rooms of rows, in order, each once.
func names(rows []*searchdemov1.RoomDay) []string {
	var out []string
	for _, r := range rows {
		if !slices.Contains(out, r.GetDisplayName()) {
			out = append(out, r.GetDisplayName())
		}
	}
	return out
}

func search_(t *testing.T, conn repox.Conn, in repox.SearchInput) []*searchdemov1.RoomDay {
	t.Helper()
	rows, _, err := search.RoomDays.Search(context.Background(), conn, in)
	if err != nil {
		t.Fatalf("Search(%+v): %v", in, err)
	}
	return rows
}

func TestProjectionSearch(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, freshDB(t))

	n, err := search.RoomDays.Count(ctx, f.conn, repox.SearchInput{})
	if err != nil || n != 9 {
		t.Fatalf("Count = %d, %v; want 9 rows (3 rooms x 3 days)", n, err)
	}

	// A is booked 09:00-10:00 on day 0: free 09:00-10:00 leaves it out.
	got := names(search_(t, f.conn, repox.SearchInput{Range: window(0, 9), OrderBy: "display_name"}))
	if want := []string{"Hot desk C", "Room B"}; !slices.Equal(got, want) {
		t.Errorf("free day0 09-10 = %v, want %v", got, want)
	}
	// 10:00-11:00 the same day it is free again.
	if got := names(search_(t, f.conn, repox.SearchInput{Range: window(0, 10), OrderBy: "display_name"})); len(got) != 3 {
		t.Errorf("free day0 10-11 = %v, want all three", got)
	}
	// B is booked all of day 1, and nothing is free at 19:00.
	if got := names(search_(t, f.conn, repox.SearchInput{Range: window(1, 12), OrderBy: "display_name"})); !slices.Equal(got, []string{"Hot desk C", "Room A"}) {
		t.Errorf("free day1 12-13 = %v", got)
	}
	if got := search_(t, f.conn, repox.SearchInput{Range: window(0, 19)}); len(got) != 0 {
		t.Errorf("free at 19:00 = %v, want none", names(got))
	}

	for _, tc := range []struct {
		filter string
		want   []string
	}{
		{`kinds:desk`, []string{"Hot desk C", "Room B"}},
		{`kinds:desk AND attributes.seats >= 5`, []string{"Room B"}},
		{`attributes.seats < 5`, []string{"Hot desk C", "Room A"}},
		{`attributes.view.sea = true`, []string{"Room A"}},
		{`attributes.view:*`, []string{"Room A"}},
		{`attributes.badge != "gold"`, []string{"Hot desk C", "Room B"}},
		{`attributes.badge:*`, []string{"Room A", "Room B"}},
		{`floor > 1 AND occupancy = FREE`, []string{"Hot desk C", "Room B"}},
		{`room`, []string{"Room A", "Room B"}}, // free text on display_name
		{`desk`, []string{"Hot desk C"}},
		{`day = "2026-11-03" AND occupancy = full`, []string{"Room B"}},
	} {
		got := names(search_(t, f.conn, repox.SearchInput{Filter: tc.filter, OrderBy: "display_name"}))
		if !slices.Equal(got, tc.want) {
			t.Errorf("filter %q = %v, want %v", tc.filter, got, tc.want)
		}
	}
	// Typed conditions: any of two values at a JSON path.
	got = names(search_(t, f.conn, repox.SearchInput{OrderBy: "display_name", Where: []filterx.Condition{filterx.Or(
		filterx.Condition{Field: "attributes.badge", Op: filterx.OpEq, Value: "gold", Quoted: true},
		filterx.Condition{Field: "attributes.seats", Op: filterx.OpEq, Value: "1"},
	)}}))
	if want := []string{"Hot desk C", "Room A"}; !slices.Equal(got, want) {
		t.Errorf("badge gold or seats 1 = %v, want %v", got, want)
	}
	if _, _, err := search.RoomDays.Search(ctx, f.conn, repox.SearchInput{Filter: `attributes.seats >= "many"`}); !errors.Is(err, repox.ErrInvalidArgument) {
		t.Errorf("a declared int path compared with text: err = %v, want ErrInvalidArgument", err)
	}
	if _, _, err := search.RoomDays.Search(ctx, f.conn, repox.SearchInput{Filter: `attributes.a'b = 1`}); !errors.Is(err, repox.ErrInvalidArgument) {
		t.Errorf("a path with a quote: err = %v, want ErrInvalidArgument", err)
	}

	// Keyset pages over floor desc, day: 9 rows in pages of 4.
	var all []*searchdemov1.RoomDay
	in := repox.SearchInput{OrderBy: "floor desc, day", PageSize: 4}
	for pages := 0; ; pages++ {
		rows, next, err := search.RoomDays.Search(ctx, f.conn, in)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, rows...)
		if next == "" {
			if pages != 2 {
				t.Errorf("pages = %d, want 3", pages+1)
			}
			break
		}
		in.PageToken = next
	}
	if len(all) != 9 || all[0].GetFloor() != 3 || all[8].GetFloor() != 1 || all[0].GetDay().GetDay() != 2 || all[2].GetDay().GetDay() != 4 {
		t.Errorf("paged rows out of order: %d rows, first floor %d day %d", len(all), all[0].GetFloor(), all[0].GetDay().GetDay())
	}

	// A row decodes whole: its free intervals, kinds and attributes.
	a := search_(t, f.conn, repox.SearchInput{Filter: `room = "` + f.a.GetName() + `" AND day = "2026-11-02"`})
	if len(a) != 1 || len(a[0].GetFree()) != 2 || a[0].GetFree()[0].GetEndTime().AsTime() != at(0, 9, 0) ||
		a[0].GetAttributes().GetFields()["seats"].GetNumberValue() != 4 || !slices.Equal(a[0].GetKinds(), []string{"meeting"}) ||
		a[0].GetOccupancy() != searchdemov1.Occupancy_OCCUPANCY_PARTIAL {
		t.Errorf("room A day 0 = %v", a)
	}

	// Facets: by tag, by enum, by JSON path, under a search.
	buckets, err := search.RoomDays.Facet(ctx, f.conn, repox.SearchInput{}, "kinds", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(buckets, []repox.Bucket{{Value: "desk", Count: 6}, {Value: "meeting", Count: 6}}) {
		t.Errorf("facet kinds = %v", buckets)
	}
	buckets, err = search.RoomDays.Facet(ctx, f.conn, repox.SearchInput{Range: window(1, 12)}, "attributes.badge", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(buckets, []repox.Bucket{{Value: "gold", Count: 1}}) {
		t.Errorf("facet attributes.badge free day1 12-13 = %v", buckets)
	}
	buckets, err = search.RoomDays.Facet(ctx, f.conn, repox.SearchInput{}, "occupancy", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(buckets, []repox.Bucket{{Value: "FREE", Count: 7}}) {
		t.Errorf("facet occupancy limit 1 = %v", buckets)
	}
}

func TestProjectionFollowsWrites(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, freshDB(t))
	free := func(r *searchdemov1.Room, d, h int) bool {
		rows := search_(t, f.conn, repox.SearchInput{Range: window(d, h), Filter: `room = "` + r.GetName() + `"`})
		return len(rows) == 1
	}
	if free(f.a, 0, 9) || !free(f.c, 0, 9) {
		t.Fatal("fixture: A should be busy and C free at day0 09:00")
	}

	// Moving A's booking to C re-projects both rooms: the writer sees the
	// booking before the write (Change.Prior) and after it.
	moved := &searchdemov1.Booking{Name: f.morningA.GetName(), Room: f.c.GetName()}
	if _, err := f.repos.Bookings.Update(ctx, moved, []string{"room"}); err != nil {
		t.Fatal(err)
	}
	if !free(f.a, 0, 9) || free(f.c, 0, 9) {
		t.Error("after the move A should be free and C busy at day0 09:00")
	}

	// Deleting it frees C again: the delete's change carries the prior.
	if err := f.repos.Bookings.Delete(ctx, f.morningA.GetName()); err != nil {
		t.Fatal(err)
	}
	if !free(f.c, 0, 9) {
		t.Error("after the delete C should be free at day0 09:00")
	}

	// An attribute change reaches the rows.
	f.a.Attributes.Fields["seats"] = structNumber(12)
	if _, err := f.repos.Rooms.Update(ctx, f.a, []string{"attributes"}); err != nil {
		t.Fatal(err)
	}
	if got := names(search_(t, f.conn, repox.SearchInput{Filter: `attributes.seats >= 10`, OrderBy: "display_name"})); !slices.Equal(got, []string{"Room A", "Room B"}) {
		t.Errorf("seats >= 10 after the update = %v", got)
	}

	// Deleting a room removes its days.
	if err := f.repos.Rooms.Delete(ctx, f.c.GetName()); err != nil {
		t.Fatal(err)
	}
	if n, _ := search.RoomDays.Count(ctx, f.conn, repox.SearchInput{}); n != 6 {
		t.Errorf("rows after deleting C = %d, want 6", n)
	}
}

func TestProjectionBatch(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, freshDB(t))
	b := search.NewBatch(f.conn)
	room, err := b.CreateRoom(&searchdemov1.Room{DisplayName: "Batch room", Floor: 9})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateBooking(booking(room, at(2, 8, 0), at(2, 12, 0))); err != nil {
		t.Fatal(err)
	}
	b.DeleteBooking(f.morningA.GetName())
	if err := b.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	rows := search_(t, f.conn, repox.SearchInput{Filter: `floor = 9 AND day = "2026-11-04"`})
	if len(rows) != 1 || rows[0].GetOccupancy() != searchdemov1.Occupancy_OCCUPANCY_PARTIAL || rows[0].GetFree()[0].GetStartTime().AsTime() != at(2, 12, 0) {
		t.Errorf("batch room day 2 = %v", rows)
	}
	// The queued delete's prior was read in the commit, so A was re-projected.
	if got := search_(t, f.conn, repox.SearchInput{Range: window(0, 9), Filter: `room = "` + f.a.GetName() + `"`}); len(got) != 1 {
		t.Error("A should be free at day0 09:00 after the batch deleted its booking")
	}
}

func TestProjectionWriterFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	db := freshDB(t)
	boom := errors.New("boom")
	conn := repox.Conn{Gorm: db, Projections: repox.NewProjections(search.RoomDays.Projector(search.RoomDayWriterFunc(
		func(context.Context, repox.Conn, []repox.Change) (search.RoomDayRows, error) {
			return search.RoomDayRows{}, boom
		})))}
	repos := search.New(conn)
	if _, err := repos.Rooms.Create(ctx, &searchdemov1.Room{DisplayName: "never"}); !errors.Is(err, boom) {
		t.Fatalf("Create = %v, want the writer's error", err)
	}
	b := search.NewBatch(conn)
	if _, err := b.CreateRoom(&searchdemov1.Room{DisplayName: "never either"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx); !errors.Is(err, boom) {
		t.Fatalf("Commit = %v, want the writer's error", err)
	}
	rooms, _, err := search.New(repox.Conn{Gorm: db}).Rooms.List(ctx, repox.ListInput{})
	if err != nil || len(rooms) != 0 {
		t.Errorf("rooms after failed writes = %d, %v; want none", len(rooms), err)
	}
}

func TestProjectionFeedAndVersions(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, freshDB(t))
	// Three room creates and two booking creates, each its own commit.
	if len(f.committed) != 5 {
		t.Fatalf("feed heard %d commits, want 5", len(f.committed))
	}
	first := f.committed[0][0]
	if first.Projection != "searchdemo.v1.RoomDay" || len(first.Stored) != 3 || len(first.Removed) != 0 {
		t.Fatalf("first commit = %+v", first)
	}
	versions := map[string]int64{}
	for _, cs := range f.committed {
		for _, c := range cs {
			for _, r := range c.Stored {
				if r.Version <= versions[r.Key] {
					t.Errorf("key %s: version %d did not rise past %d", r.Key, r.Version, versions[r.Key])
				}
				versions[r.Key] = r.Version
				if _, ok := r.Row.(*searchdemov1.RoomDay); !ok {
					t.Errorf("stored row is %T", r.Row)
				}
			}
		}
	}
	// Re-projecting a room replaces its rows: a prune and upsert of the same
	// keys reaches the feed as stores only, versions raised.
	f.committed = nil
	if _, err := f.repos.Rooms.Update(ctx, &searchdemov1.Room{Name: f.a.GetName(), Floor: 7}, []string{"floor"}); err != nil {
		t.Fatal(err)
	}
	if len(f.committed) != 1 || len(f.committed[0][0].Stored) != 3 || len(f.committed[0][0].Removed) != 0 {
		t.Fatalf("update commit = %+v", f.committed)
	}
	for _, r := range f.committed[0][0].Stored {
		if r.Version <= versions[r.Key] {
			t.Errorf("key %s: version %d not above %d", r.Key, r.Version, versions[r.Key])
		}
		versions[r.Key] = r.Version
	}
	// A delete reaches it as removals versioned past the rows.
	f.committed = nil
	if err := f.repos.Rooms.Delete(ctx, f.c.GetName()); err != nil {
		t.Fatal(err)
	}
	if len(f.committed) != 1 || len(f.committed[0][0].Removed) != 3 {
		t.Fatalf("delete commit = %+v", f.committed)
	}
	for _, r := range f.committed[0][0].Removed {
		if r.Version <= versions[r.Key] {
			t.Errorf("removal of %s: version %d not above %d", r.Key, r.Version, versions[r.Key])
		}
	}
}

func TestProjectionRebuild(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, freshDB(t))
	if err := f.conn.Gorm.Exec(`UPDATE "search"."room_days" SET floor = 99`).Error; err != nil {
		t.Fatal(err)
	}
	rooms, _, err := search.New(repox.Conn{Gorm: f.conn.Gorm}).Rooms.List(ctx, repox.ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	var batches iter.Seq[[]repox.Change] = func(yield func([]repox.Change) bool) {
		for _, r := range rooms {
			if !yield([]repox.Change{{Type: roomType, Name: r.GetName(), Op: repox.ChangeUpdate}}) {
				return
			}
		}
	}
	res, err := search.RoomDays.Rebuild(ctx, f.conn, roomDays(), batches, repox.RebuildOptions{Truncate: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Batches != 3 || res.Upserted != 9 || res.Deleted != 9 {
		t.Errorf("Rebuild = %+v, want 3 batches, 9 upserted, 9 deleted", res)
	}
	if n, _ := search.RoomDays.Count(ctx, f.conn, repox.SearchInput{Filter: "floor = 99"}); n != 0 {
		t.Errorf("%d drifted rows survived the rebuild", n)
	}
}

func TestListByKeys(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, freshDB(t))
	defer func(k, p int) { repox.KeysPerRead, repox.ReadParallelism = k, p }(repox.KeysPerRead, repox.ReadParallelism)
	repox.KeysPerRead, repox.ReadParallelism = 1, 2
	keys := []string{f.a.GetName(), f.b.GetName(), f.c.GetName(), f.a.GetName(), "rooms/01JZZZZZZZZZZZZZZZZZZZZZZZ"}
	got, err := f.repos.Rooms.ListByKeys(ctx, "name", keys, repox.ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	var gotNames []string
	for _, r := range got {
		gotNames = append(gotNames, r.GetName())
	}
	if want := []string{f.a.GetName(), f.b.GetName(), f.c.GetName()}; !slices.Equal(gotNames, want) {
		t.Errorf("ListByKeys = %v, want %v (key order, deduplicated)", gotNames, want)
	}
	// in narrows every chunk.
	got, err = f.repos.Rooms.ListByKeys(ctx, "name", keys, repox.ListInput{Filter: "floor >= 2", ReadMask: []string{"floor"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].GetDisplayName() != "" || got[0].GetFloor() != 2 {
		t.Errorf("ListByKeys narrowed = %v", got)
	}
	// Bookings by room, a reference field.
	bs, err := f.repos.Bookings.ListByKeys(ctx, "room", []string{f.a.GetName(), f.b.GetName()}, repox.ListInput{})
	if err != nil || len(bs) != 2 {
		t.Errorf("bookings by room = %d, %v", len(bs), err)
	}
}
