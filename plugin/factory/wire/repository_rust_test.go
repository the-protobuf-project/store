// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package wire_test

// repository_rust_test.go pins lang=rust target=repository beyond its goldens
// (testdata/cases/rust_*): what it refuses, and that its output is what
// rustfmt would write.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/golden"
	"github.com/the-protobuf-project/store/plugin/factory/target/repository"
)

// TestRustRepositoryRefusesChildStorage pins that a resource whose storage
// the records runtime cannot write yet -- child tables, join tables, message
// columns, oneof case columns -- fails generation by field name rather than
// generating a repository that drops the field.
func TestRustRepositoryRefusesChildStorage(t *testing.T) {
	for _, c := range []struct{ dir, mentions string }{
		{"versionedpkg", `resource.v1.Event.window: stored in a child table (column "window_id" of events)`},
		{"deepgraph", `deep.v1.Agenda.slots: stored in rows of a child table`},
		{"references", `refdemo.catalog.v1.Book.filed_under: stored in the join table "book_filed_under"`},
		{"embedded", `embedded.v1.Event.attendees: stored in the join table "event_attendees"`},
		{"oneof", `oneof.v1.Audio.input: the oneof's case is stored in column "input_case" of audios`},
		{"repository", `org.v1.Member.duration: a google.protobuf.Duration field (column "duration")`},
		{"datetimezone", `dtz.v1.Session.starts: a google.type.DateTime field (column "starts")`},
		{"documents", `stay.v1.Booking.stay: a vocab.v1.Stay field (column "stay")`},
	} {
		t.Run(c.dir, func(t *testing.T) {
			err := runRustRepository(t, filepath.Join("testdata", "cases", c.dir))
			if err == nil || !strings.Contains(err.Error(), c.mentions) {
				t.Errorf("want an error mentioning %q, got %v", c.mentions, err)
			}
		})
	}
}

func runRustRepository(t *testing.T, dir string) error {
	t.Helper()
	p, err := protogen.Options{}.New(golden.BuildRequest(t, dir))
	if err != nil {
		t.Fatalf("protogen: %v", err)
	}
	pl := ormCasePlugin(dir)
	pl.Registry["repository"] = &repository.Generator{Lang: "rust"}
	return protokit.RunPlugin(p, protokit.Options{Target: "repository"}, pl)
}

// TestRustRepositoryGoldensAreRustfmtClean requires every Rust golden to be
// what rustfmt (edition 2024) would write, so generated crates pass a
// `cargo fmt --check`. Skipped where rustfmt is not installed.
func TestRustRepositoryGoldensAreRustfmtClean(t *testing.T) {
	rustfmt, err := exec.LookPath("rustfmt")
	if err != nil {
		t.Skip("rustfmt not on PATH")
	}
	libs, err := filepath.Glob(filepath.Join("testdata", "cases", "rust_*", "golden", "repository", "lib.rs"))
	if err != nil || len(libs) == 0 {
		t.Fatalf("no Rust repository goldens found (%v)", err)
	}
	for _, lib := range libs {
		// rustfmt follows lib.rs's `pub mod` declarations into every module,
		// so it runs over a copy holding an empty cache.rs wherever a package
		// mounts protoc-gen-cache's, which this plugin does not write.
		dir := t.TempDir()
		if err := os.CopyFS(dir, os.DirFS(filepath.Dir(lib))); err != nil {
			t.Fatal(err)
		}
		mods, _ := filepath.Glob(filepath.Join(dir, "*", "*", "mod.rs"))
		for _, mod := range mods {
			b, _ := os.ReadFile(mod)
			if strings.Contains(string(b), "\npub mod cache;\n") {
				if err := os.WriteFile(filepath.Join(filepath.Dir(mod), "cache.rs"), []byte("//! protoc-gen-cache's.\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		out, err := exec.Command(rustfmt, "--check", "--edition", "2024", filepath.Join(dir, "lib.rs")).CombinedOutput()
		if err != nil {
			t.Errorf("%s is not rustfmt-clean:\n%s", filepath.Dir(lib), out)
		}
	}
}
