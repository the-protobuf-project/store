// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package search_test

// interval_test.go checks that a plain google.type.Interval field -- not a
// projection's range -- is a text column, and that a value written through
// the repository reads back unchanged. The converters store "start/end"
// RFC 3339 text, which a tstzrange column would reject. (The AutoMigrate
// path is checked in automigrate_test.go.)

import (
	"context"
	"testing"

	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/the-protobuf-project/store/examples/search/gen/repository/repox"
	"github.com/the-protobuf-project/store/examples/search/gen/repository/search/search"
)

func TestPlainIntervalRoundTrips(t *testing.T) {
	ctx := context.Background()
	db := freshDB(t)
	var typ string
	if err := db.Raw(`SELECT data_type FROM information_schema.columns WHERE table_schema = 'search' AND table_name = 'bookings' AND column_name = 'setup_window'`).Scan(&typ).Error; err != nil || typ != "text" {
		t.Fatalf("setup_window column type = %q, %v; want text", typ, err)
	}
	repos := search.New(repox.Conn{Gorm: db})
	room := mustRoom(t, repos, "Room A", 1, nil, map[string]any{})
	in := booking(room.GetName(), at(0, 9, 0), at(0, 10, 0))
	in.SetupWindow = &interval.Interval{StartTime: timestamppb.New(at(0, 8, 30)), EndTime: timestamppb.New(at(0, 10, 15))}
	created, err := repos.Bookings.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repos.Bookings.Get(ctx, created.GetName())
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got.GetSetupWindow(), in.GetSetupWindow()) {
		t.Fatalf("setup_window = %v, want %v", got.GetSetupWindow(), in.GetSetupWindow())
	}
	var stored string
	if err := db.Raw(`SELECT setup_window FROM search.bookings`).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if want := at(0, 8, 30).UTC().Format("2006-01-02T15:04:05Z07:00") + "/" + at(0, 10, 15).UTC().Format("2006-01-02T15:04:05Z07:00"); stored != want {
		t.Errorf("stored setup_window = %q, want %q", stored, want)
	}
	got.SetupWindow.EndTime = timestamppb.New(at(0, 11, 0))
	updated, err := repos.Bookings.Update(ctx, got, []string{"setup_window"})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.GetSetupWindow().GetEndTime().AsTime().Equal(at(0, 11, 0)) {
		t.Errorf("updated setup_window = %v", updated.GetSetupWindow())
	}
}
