// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package search_test

// automigrate_test.go migrates the search example with the GORM registry
// instead of migrate.sql: the projection's multirange column and its GiST,
// GIN and expression indexes come from the model's tags.

import (
	"testing"

	"github.com/the-protobuf-project/store/examples/search/gen/gorm/search"
)

func TestAutoMigrateProjection(t *testing.T) {
	db := emptyDB(t)
	if err := search.Default.EnsureSchemas(db); err != nil {
		t.Fatal(err)
	}
	if err := search.Default.Migrate(db); err != nil {
		t.Fatal(err)
	}
	var typ string
	if err := db.Raw(`SELECT data_type FROM information_schema.columns WHERE table_schema = 'search' AND table_name = 'room_days' AND column_name = 'free'`).Scan(&typ).Error; err != nil || typ != "tstzmultirange" {
		t.Errorf("free column type = %q, %v; want tstzmultirange", typ, err)
	}
	var methods []string
	if err := db.Raw(`SELECT am.amname || ':' || i.relname FROM pg_index x JOIN pg_class i ON i.oid = x.indexrelid JOIN pg_class t ON t.oid = x.indrelid JOIN pg_am am ON am.oid = i.relam WHERE t.relname = 'room_days' ORDER BY i.relname`).Scan(&methods).Error; err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gist:idx_room_days_free_gist", "gin:idx_room_days_kinds_gin", "gin:idx_room_days_attributes_gin", "btree:idx_room_days_seats", "btree:idx_room_days_room_day"} {
		found := false
		for _, m := range methods {
			found = found || m == want
		}
		if !found {
			t.Errorf("index %s missing; have %v", want, methods)
		}
	}
}
