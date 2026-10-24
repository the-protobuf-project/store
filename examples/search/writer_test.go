// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package search_test

// writer_test.go is the application side of the RoomDay projection: the
// writer computing each room's free time per day from the room and its
// bookings, over a fixed three-day window and opening hours, plus fixtures.

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/the-protobuf-project/store/examples/search/gen/gorm/filterx"
	"github.com/the-protobuf-project/store/examples/search/gen/pb/searchdemov1"
	"github.com/the-protobuf-project/store/examples/search/gen/repository/repox"
	"github.com/the-protobuf-project/store/examples/search/gen/repository/search/search"
)

// days are the days the projection covers; rooms are open 08:00-18:00 UTC.
var days = []time.Time{
	time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
	time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC),
	time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC),
}

const (
	roomType    = "searchdemo.v1/Room"
	bookingType = "searchdemo.v1/Booking"
)

// at is an instant on the first day.
func at(day, hour, minute int) time.Time {
	return days[day].Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
}

// roomDays is the RoomDay writer: every room a change touches -- a room
// written, the room of a booking before and after the write -- is pruned and
// projected again from what the write's transaction sees.
func roomDays() search.RoomDayWriterFunc {
	return func(ctx context.Context, read repox.Conn, changes []repox.Change) (search.RoomDayRows, error) {
		rooms := map[string]bool{}
		for _, c := range changes {
			switch c.Type {
			case roomType:
				rooms[c.Name] = true
			case bookingType:
				for _, m := range []any{c.Record, c.Prior} {
					if b, ok := m.(*searchdemov1.Booking); ok && b.GetRoom() != "" {
						rooms[b.GetRoom()] = true
					}
				}
				if c.Record == nil && c.Prior == nil {
					return search.RoomDayRows{}, errors.New("a booking change with neither record nor prior")
				}
			}
		}
		var out search.RoomDayRows
		repos := search.New(read)
		for room := range rooms {
			out.Prune = append(out.Prune, []filterx.Condition{{Field: "room", Op: filterx.OpEq, Value: room}})
			r, err := repos.Rooms.Get(ctx, room)
			if errors.Is(err, repox.ErrNotFound) {
				continue
			}
			if err != nil {
				return out, err
			}
			bookings, err := repos.Bookings.ListByKeys(ctx, "room", []string{room}, repox.ListInput{})
			if err != nil {
				return out, err
			}
			for _, d := range days {
				out.Upsert = append(out.Upsert, dayOf(r, d, bookings))
			}
		}
		return out, nil
	}
}

// dayOf is room r's RoomDay on day d: the opening hours less the bookings.
func dayOf(r *searchdemov1.Room, d time.Time, bookings []*searchdemov1.Booking) *searchdemov1.RoomDay {
	open, closed := d.Add(8*time.Hour), d.Add(18*time.Hour)
	type span struct{ s, e time.Time }
	var busy []span
	for _, b := range bookings {
		s, e := b.GetStartTime().AsTime(), b.GetEndTime().AsTime()
		if e.After(open) && s.Before(closed) {
			busy = append(busy, span{maxTime(s, open), minTime(e, closed)})
		}
	}
	slices.SortFunc(busy, func(a, b span) int { return a.s.Compare(b.s) })
	var free []*interval.Interval
	cur := open
	for _, b := range busy {
		if b.s.After(cur) {
			free = append(free, &interval.Interval{StartTime: timestamppb.New(cur), EndTime: timestamppb.New(b.s)})
		}
		cur = maxTime(cur, b.e)
	}
	if closed.After(cur) {
		free = append(free, &interval.Interval{StartTime: timestamppb.New(cur), EndTime: timestamppb.New(closed)})
	}
	occ := searchdemov1.Occupancy_OCCUPANCY_PARTIAL
	switch {
	case len(busy) == 0:
		occ = searchdemov1.Occupancy_OCCUPANCY_FREE
	case len(free) == 0:
		occ = searchdemov1.Occupancy_OCCUPANCY_FULL
	}
	return &searchdemov1.RoomDay{
		Room:        r.GetName(),
		Day:         &date.Date{Year: int32(d.Year()), Month: int32(d.Month()), Day: int32(d.Day())},
		Free:        free,
		Kinds:       r.GetKinds(),
		Floor:       r.GetFloor(),
		Attributes:  r.GetAttributes(),
		Occupancy:   occ,
		DisplayName: r.GetDisplayName(),
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// fixture is three rooms and their bookings, written through repositories
// that maintain the projection.
type fixture struct {
	conn      repox.Conn
	repos     search.Repositories
	a, b, c   *searchdemov1.Room
	morningA  *searchdemov1.Booking
	committed [][]repox.Committed
}

func newFixture(t *testing.T, db *gorm.DB, feeds ...repox.ProjectionFeed) *fixture {
	t.Helper()
	ctx := context.Background()
	f := &fixture{}
	f.conn = repox.Conn{Gorm: db, Projections: repox.NewProjections(search.RoomDays.Projector(roomDays()))}
	f.conn.Projections.Feed = repox.Feeds(append([]repox.ProjectionFeed{repox.ProjectionFeedFunc(func(_ context.Context, cs []repox.Committed) {
		f.committed = append(f.committed, cs)
	})}, feeds...)...)
	f.repos = search.New(f.conn)
	f.a = mustRoom(t, f.repos, "Room A", 1, []string{"meeting"}, map[string]any{"seats": 4, "view": map[string]any{"sea": true}, "badge": "gold"})
	f.b = mustRoom(t, f.repos, "Room B", 2, []string{"meeting", "desk"}, map[string]any{"seats": 10, "badge": "silver"})
	f.c = mustRoom(t, f.repos, "Hot desk C", 3, []string{"desk"}, map[string]any{"seats": 1})
	var err error
	// A is booked 09:00-10:00 on the first day, B all of the second day.
	if f.morningA, err = f.repos.Bookings.Create(ctx, booking(f.a.GetName(), at(0, 9, 0), at(0, 10, 0))); err != nil {
		t.Fatal(err)
	}
	if _, err = f.repos.Bookings.Create(ctx, booking(f.b.GetName(), at(1, 7, 0), at(1, 19, 0))); err != nil {
		t.Fatal(err)
	}
	return f
}

func mustRoom(t *testing.T, repos search.Repositories, name string, floor int32, kinds []string, attrs map[string]any) *searchdemov1.Room {
	t.Helper()
	s, err := structpb.NewStruct(attrs)
	if err != nil {
		t.Fatal(err)
	}
	r, err := repos.Rooms.Create(context.Background(), &searchdemov1.Room{DisplayName: name, Floor: floor, Kinds: kinds, Attributes: s})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func booking(room string, start, end time.Time) *searchdemov1.Booking {
	return &searchdemov1.Booking{Room: room, StartTime: timestamppb.New(start), EndTime: timestamppb.New(end)}
}

func structNumber(n float64) *structpb.Value { return structpb.NewNumberValue(n) }

// roomDayFacet is the Postgres facet of field over the rows filter matches.
func (f *fixture) roomDayFacet(ctx context.Context, filter, field string) ([]repox.Bucket, error) {
	return search.RoomDays.Facet(ctx, f.conn, repox.SearchInput{Filter: filter}, field, 0)
}
