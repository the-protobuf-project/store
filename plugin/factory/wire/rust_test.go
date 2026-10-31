// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package wire_test

import (
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/golden"
)

// TestRustRejectsBadTimeSeries pins that a time_series message a point cannot
// hold fails the rust target by name, rather than generating a model that the
// runtime refuses on its first write or that quietly drops a field. Fixtures
// live outside testdata/cases so TestGolden ignores them.
func TestRustRejectsBadTimeSeries(t *testing.T) {
	cases := []struct{ dir, mentions string }{
		{"time_missing", `time_series.time "when" names no field`},
		{"time_type", `time_series.time "device" must be a google.protobuf.Timestamp or an int64`},
		{"tag_type", `time_series.tags "zone" must be a string field`},
		{"tag_is_time", `time_series.tags "at" is the time field`},
		{"tag_twice", `time_series.tags lists "device" twice`},
		{"repeated", `field "values": repeated fields cannot be stored in a point`},
		{"wrapper", `field "value": google.protobuf.DoubleValue cannot be stored in a point`},
		{"optional", `field "value": explicit presence (optional) cannot be stored in a point`},
		{"oneof", `field "value": oneof members cannot be stored in a point`},
		{"bytes", `field "payload": bytes fields have no tpp-database kind`},
		{"child", `field "where" becomes no column of its own`},
		{"no_fields", `needs at least one field that is neither the time nor a tag`},
	}
	for _, c := range cases {
		t.Run(c.dir, func(t *testing.T) {
			req := golden.BuildRequest(t, filepath.Join("testdata", "timeseries_bad", c.dir))
			p, err := protogen.Options{}.New(req)
			if err != nil {
				t.Fatalf("protogen: %v", err)
			}
			err = protokit.RunPlugin(p, protokit.Options{Target: "rust"}, ormPlugin())
			if err == nil || !strings.Contains(err.Error(), c.mentions) {
				t.Errorf("want an error mentioning %q, got %v", c.mentions, err)
			}
			if err != nil && !strings.Contains(err.Error(), "bad.v1.Sample") {
				t.Errorf("error does not name the message: %v", err)
			}
		})
	}
}

// TestRustRejectsTableCollision pins that two schemas of one database
// declaring the same table fail generation: InfluxDB has no schemas, so both
// would write into one table.
func TestRustRejectsTableCollision(t *testing.T) {
	req := golden.BuildRequest(t, filepath.Join("testdata", "timeseries_bad_collision"))
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen: %v", err)
	}
	err = protokit.RunPlugin(p, protokit.Options{Target: "rust"}, ormPlugin())
	want := `alpha.v1.Sample and beta.v1.Sample both store table "samples" in database "shared_db"`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("want an error mentioning %q, got %v", want, err)
	}
}

// TestRustSkipsRelationalDatabases pins that a database declaring no time
// series renders nothing on the rust target, so the target can run over a
// mixed proto tree without failing on its relational messages.
func TestRustSkipsRelationalDatabases(t *testing.T) {
	req := golden.BuildRequest(t, filepath.Join("testdata", "cases", "bookstore"))
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen: %v", err)
	}
	if err := protokit.RunPlugin(p, protokit.Options{Target: "rust"}, ormPlugin()); err != nil {
		t.Fatalf("rust on a relational tree: %v", err)
	}
	if files := p.Response().GetFile(); len(files) != 0 {
		t.Errorf("rust on a relational tree wrote %d files, want none (first: %s)", len(files), files[0].GetName())
	}
}
