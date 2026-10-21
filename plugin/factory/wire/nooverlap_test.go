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

// TestNoOverlapRejectsBadRules pins that a no_overlap rule the generator cannot
// render fails generation on every target that renders one, rather than
// leaving the table quietly unguarded. Fixtures live outside testdata/cases so
// TestGolden ignores them.
func TestNoOverlapRejectsBadRules(t *testing.T) {
	cases := []struct{ dir, mentions string }{
		{"key", `key "cuort" names no field`},
		{"type", `start "court" is not a google.protobuf.Timestamp`},
	}
	for _, c := range cases {
		req := golden.BuildRequest(t, filepath.Join("testdata", "nooverlap_bad", c.dir))
		for _, target := range []string{"gorm", "sql"} {
			p, err := protogen.Options{}.New(req)
			if err != nil {
				t.Fatalf("protogen: %v", err)
			}
			err = protokit.RunPlugin(p, protokit.Options{Target: target}, ormPlugin())
			if err == nil || !strings.Contains(err.Error(), c.mentions) {
				t.Errorf("%s on %s: want an error mentioning %q, got %v", target, c.dir, c.mentions, err)
			}
		}
	}
}
