// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package migrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestMarkNotValid(t *testing.T) {
	pending := []fkConstraint{
		{"allocation", "resource", "fk_resource_resource"},
		{"allocation", "resource", "fk_resource_organizational_unit_id"},
		{"allocation", "holds", "fk_holds_organizational_unit_id"},
	}
	src := `-- Modify "holds" table
ALTER TABLE "allocation"."holds" ADD CONSTRAINT "fk_holds_organizational_unit_id" FOREIGN KEY ("organizational_unit_id") REFERENCES "orgunit"."organizational_units" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION;
-- Modify "resource" table
ALTER TABLE "allocation"."resource" ADD CONSTRAINT "fk_resource_organizational_unit_id" FOREIGN KEY ("organizational_unit_id") REFERENCES "orgunit"."organizational_units" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION, ADD CONSTRAINT "fk_resource_resource" FOREIGN KEY ("resource") REFERENCES "resource"."entity" ("name") ON UPDATE NO ACTION ON DELETE NO ACTION;
-- Modify "other" table
ALTER TABLE "allocation"."other" ADD CONSTRAINT "fk_resource_resource" FOREIGN KEY ("resource") REFERENCES "resource"."entity" ("name") ON UPDATE NO ACTION ON DELETE NO ACTION;
ALTER TABLE "allocation"."resource" ADD CONSTRAINT "fk_kept" FOREIGN KEY ("x") REFERENCES "a"."b" ("y") ON UPDATE NO ACTION ON DELETE NO ACTION;
`
	want := `-- Modify "holds" table
ALTER TABLE "allocation"."holds" ADD CONSTRAINT "fk_holds_organizational_unit_id" FOREIGN KEY ("organizational_unit_id") REFERENCES "orgunit"."organizational_units" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT VALID;
-- Modify "resource" table
ALTER TABLE "allocation"."resource" ADD CONSTRAINT "fk_resource_organizational_unit_id" FOREIGN KEY ("organizational_unit_id") REFERENCES "orgunit"."organizational_units" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT VALID, ADD CONSTRAINT "fk_resource_resource" FOREIGN KEY ("resource") REFERENCES "resource"."entity" ("name") ON UPDATE NO ACTION ON DELETE NO ACTION NOT VALID;
-- Modify "other" table
ALTER TABLE "allocation"."other" ADD CONSTRAINT "fk_resource_resource" FOREIGN KEY ("resource") REFERENCES "resource"."entity" ("name") ON UPDATE NO ACTION ON DELETE NO ACTION;
ALTER TABLE "allocation"."resource" ADD CONSTRAINT "fk_kept" FOREIGN KEY ("x") REFERENCES "a"."b" ("y") ON UPDATE NO ACTION ON DELETE NO ACTION;
`
	if got := markNotValid(src, pending); got != want {
		t.Errorf("markNotValid:\n got %s\nwant %s", got, want)
	}
	if got := markNotValid(want, nil); got != want {
		t.Errorf("markNotValid with nothing pending changed the file")
	}
}

func TestValidateStatements(t *testing.T) {
	pending := []fkConstraint{
		{"allocation", "holds", "fk_holds_organizational_unit_id"},
		{"allocation", "resource", "fk_resource_resource"},
	}
	history := `ALTER TABLE "allocation"."resource" VALIDATE CONSTRAINT "fk_resource_resource";`
	got := validateStatements(pending, history)
	want := `-- Validate "fk_holds_organizational_unit_id" on "allocation"."holds"
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_holds_organizational_unit_id' AND conrelid = to_regclass('"allocation"."holds"') AND NOT convalidated) THEN
    ALTER TABLE "allocation"."holds" VALIDATE CONSTRAINT "fk_holds_organizational_unit_id";
  END IF;
END $$;
`
	if len(got) != 1 || got[0] != want {
		t.Errorf("validateStatements = %q, want [%q]", got, want)
	}
}

func TestExtensionStatements(t *testing.T) {
	history := "-- Create extension \"btree_gist\"\nCREATE EXTENSION IF NOT EXISTS \"btree_gist\";\ncreate extension if not exists \"citext\";\n"
	got := extensionStatements([]string{"btree_gist", "citext", "pg_trgm"}, history)
	want := []string{"-- Create extension \"pg_trgm\"\nCREATE EXTENSION IF NOT EXISTS \"pg_trgm\";\n"}
	if !slices.Equal(got, want) {
		t.Errorf("extensionStatements = %q, want %q", got, want)
	}
}

func TestRoutineStatements(t *testing.T) {
	fn := "CREATE OR REPLACE FUNCTION a.f()\n RETURNS trigger\n LANGUAGE plpgsql\nAS $function$\nBEGIN\n  RETURN NEW;\nEND $function$\n"
	changed := "CREATE OR REPLACE FUNCTION a.g()\n RETURNS trigger\n LANGUAGE plpgsql\nAS $function$\nBEGIN\n  RETURN OLD;\nEND $function$\n"
	trig := trigger{Name: "t", Table: "a.r", Def: "CREATE TRIGGER t BEFORE INSERT ON a.r FOR EACH ROW EXECUTE FUNCTION a.f()"}
	newTrig := trigger{Name: "u", Table: "a.r", Def: "CREATE TRIGGER u BEFORE UPDATE ON a.r FOR EACH ROW EXECUTE FUNCTION a.g()"}
	history := strings.TrimSpace(fn) + ";\n" + strings.Replace(changed, "OLD", "NEW", 1) + ";\n" +
		"DROP TRIGGER IF EXISTS \"t\" ON a.r;\n" + trig.Def + ";\n"
	got := routineStatements([]string{fn, changed}, []trigger{trig, newTrig}, history)
	want := []string{
		strings.TrimSpace(changed) + ";\n",
		"DROP TRIGGER IF EXISTS \"u\" ON a.r;\n" + newTrig.Def + ";\n",
	}
	if !slices.Equal(got, want) {
		t.Errorf("routineStatements = %q, want %q", got, want)
	}
}

func TestWithHeader(t *testing.T) {
	h := "-- Copyright 2026 Example.\n-- SPDX-License-Identifier: Apache-2.0"
	got := withHeader("CREATE TABLE x ();\n", h)
	want := h + "\n\nCREATE TABLE x ();\n"
	if got != want {
		t.Errorf("withHeader = %q, want %q", got, want)
	}
	if again := withHeader(got, h+"\n"); again != got {
		t.Errorf("withHeader applied twice: %q", again)
	}
}

func TestDatabaseURLs(t *testing.T) {
	u := "postgres://me:p%40ss@localhost:5432/app?sslmode=disable"
	name, err := databaseOf(u)
	if err != nil || name != "app" {
		t.Fatalf("databaseOf = %q, %v", name, err)
	}
	got, err := withDatabase(u, "app_atlas_dev")
	if err != nil {
		t.Fatal(err)
	}
	if want := "postgres://me:p%40ss@localhost:5432/app_atlas_dev?sslmode=disable"; got != want {
		t.Errorf("withDatabase = %q, want %q", got, want)
	}
	if _, err := databaseOf("postgres://localhost:5432"); err == nil {
		t.Error("databaseOf without a database: no error")
	}
	if _, err := withDatabase("mysql://localhost/app", "x"); err == nil {
		t.Error("withDatabase on mysql://: no error")
	}
}

func TestAtlasVersionMatches(t *testing.T) {
	out := "atlas community version v1.3.0\nhttps://github.com/ariga/atlas/releases/tag/v1.3.0\n"
	for want, ok := range map[string]bool{"v1.3.0": true, "1.3.0": true, "v1.3": false, "v1.3.1": false} {
		if got := atlasVersionMatches(out, want); got != ok {
			t.Errorf("atlasVersionMatches(%q) = %v, want %v", want, got, ok)
		}
	}
}

func TestConfig(t *testing.T) {
	t.Setenv("MIGRATE_TEST_PASSWORD", "secret")
	path := filepath.Join(t.TempDir(), "migrate.yaml")
	if err := os.WriteFile(path, []byte("url: postgres://me:${MIGRATE_TEST_PASSWORD}@db:5432/app\ndir: db/migrations\natlas_version: v1.3.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fc, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	got := Config{Dir: "x", Header: "-- h"}.merge(fc).merge(Config{Dir: "y"}).withDefaults()
	want := Config{
		URL:          "postgres://me:secret@db:5432/app",
		ScratchURL:   "postgres://me:secret@db:5432/app",
		Dir:          "y",
		Header:       "-- h",
		Atlas:        "atlas",
		AtlasVersion: "v1.3.0",
	}
	if got != want {
		t.Errorf("config = %+v, want %+v", got, want)
	}
	if err := os.WriteFile(path, []byte("unknown: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Error("LoadConfig with an unknown key: no error")
	}
}

func TestRunUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"diff"}, {"diff", "a", "b"}, {"check", "x"}, {"validate", "x"}, {"nope"}, {"-dir", "x"}} {
		if err := Run(context.Background(), Config{}, args); !errors.Is(err, errUsage) {
			t.Errorf("Run(%q) = %v, want the usage error", args, err)
		}
	}
	if err := Run(context.Background(), Config{}, []string{"apply"}); err == nil || errors.Is(err, errUsage) {
		t.Errorf("apply without a URL = %v, want a missing-URL error", err)
	}
}

// fakeMigrator stands in for a generated package's Migrator interface.
type fakeMigrator interface{ AutoMigrate(dst ...any) error }

// fakeRegistry records the calls Build makes, in order.
type fakeRegistry struct {
	name  string
	calls *[]string
}

func (r fakeRegistry) EnsureSchemas(*gorm.DB) error {
	*r.calls = append(*r.calls, r.name+".EnsureSchemas")
	return nil
}

func (r fakeRegistry) Migrate(db fakeMigrator) error {
	if db == nil {
		return errors.New("nil migrator")
	}
	*r.calls = append(*r.calls, r.name+".Migrate")
	return nil
}

func (r fakeRegistry) MigrateExternal(*gorm.DB) error {
	*r.calls = append(*r.calls, r.name+".MigrateExternal")
	return nil
}

func TestBuildOrder(t *testing.T) {
	var calls []string
	err := Build(&gorm.DB{}, Of(fakeRegistry{"a", &calls}), Of(fakeRegistry{"b", &calls}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.EnsureSchemas", "a.Migrate", "b.EnsureSchemas", "b.Migrate", "a.MigrateExternal", "b.MigrateExternal"}
	if !slices.Equal(calls, want) {
		t.Errorf("Build called %q, want %q", calls, want)
	}
}

// badRegistry's Migrate takes something *gorm.DB is not.
type badRegistry struct{}

func (badRegistry) EnsureSchemas(*gorm.DB) error   { return nil }
func (badRegistry) Migrate(int) error              { return nil }
func (badRegistry) MigrateExternal(*gorm.DB) error { return nil }

func TestOfRejectsForeignMigrator(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Of(badRegistry) did not panic")
		}
	}()
	Of(badRegistry{})
}
