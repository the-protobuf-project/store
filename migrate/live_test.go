// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package migrate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

// The live test drives the whole cycle against a real Postgres server and
// atlas CLI. It runs only when STORE_MIGRATE_TEST_URL names a database on a
// server where it may create and drop databases, e.g.
//
//	STORE_MIGRATE_TEST_URL=postgres://postgres@localhost:55432/storemigrate?sslmode=disable go test ./migrate -run Live

// liveWidget is the table every stage has.
type liveWidget struct {
	ID    int64  `gorm:"primaryKey"`
	Name  string `gorm:"index"`
	Owner *int64
}

func (liveWidget) TableName() string { return "app.widgets" }

// liveOwner is the table of the second registry, which stage 3 adds.
type liveOwner struct {
	ID int64 `gorm:"primaryKey"`
}

func (liveOwner) TableName() string { return "ext.owners" }

// liveRegistry plays a generated registry whose models grow by stage: 1 is a
// table and an extension, 2 adds a trigger function and its trigger, 3 adds a
// foreign key into the other registry's table, added NOT VALID.
type liveRegistry struct{ stage *int }

func (r liveRegistry) EnsureSchemas(db *gorm.DB) error {
	if err := db.Exec(`CREATE SCHEMA IF NOT EXISTS "app"`).Error; err != nil {
		return err
	}
	return db.Exec(`CREATE EXTENSION IF NOT EXISTS "pg_trgm"`).Error
}

func (r liveRegistry) Migrate(db fakeMigrator) error {
	if err := db.AutoMigrate(&liveWidget{}); err != nil {
		return err
	}
	if *r.stage < 2 {
		return nil
	}
	g := db.(*gorm.DB)
	if err := g.Exec(`CREATE OR REPLACE FUNCTION "app"."widgets_lock"() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended(NEW."name", 0));
  RETURN NEW;
END $$;`).Error; err != nil {
		return err
	}
	return g.Exec(`DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'widgets_lock' AND tgrelid = '"app"."widgets"'::regclass) THEN
    CREATE TRIGGER "widgets_lock" BEFORE INSERT OR UPDATE OF "name" ON "app"."widgets" FOR EACH ROW EXECUTE FUNCTION "app"."widgets_lock"();
  END IF;
END $$;`).Error
}

func (r liveRegistry) MigrateExternal(db *gorm.DB) error {
	if *r.stage < 3 {
		return nil
	}
	return db.Exec(`DO $$ BEGIN
  IF to_regclass('"ext"."owners"') IS NOT NULL AND NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_widgets_owner') THEN
    ALTER TABLE "app"."widgets" ADD CONSTRAINT "fk_widgets_owner" FOREIGN KEY ("owner") REFERENCES "ext"."owners"("id") NOT VALID;
  END IF;
END $$;`).Error
}

// liveOwners is the second registry, present from stage 3.
type liveOwners struct{ stage *int }

func (r liveOwners) EnsureSchemas(db *gorm.DB) error {
	if *r.stage < 3 {
		return nil
	}
	return db.Exec(`CREATE SCHEMA IF NOT EXISTS "ext"`).Error
}

func (r liveOwners) Migrate(db fakeMigrator) error {
	if *r.stage < 3 {
		return nil
	}
	return db.AutoMigrate(&liveOwner{})
}

func (liveOwners) MigrateExternal(*gorm.DB) error { return nil }

func TestLive(t *testing.T) {
	base := os.Getenv("STORE_MIGRATE_TEST_URL")
	if base == "" {
		t.Skip("STORE_MIGRATE_TEST_URL is not set")
	}
	if _, err := exec.LookPath("atlas"); err != nil {
		t.Skip("atlas is not on PATH")
	}
	ctx := context.Background()

	// The database apply migrates, fresh.
	admin, err := Open(base)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(admin)
	baseName, err := databaseOf(base)
	if err != nil {
		t.Fatal(err)
	}
	targetName := baseName + "_target"
	if err := recreate(admin, targetName); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Exec(`DROP DATABASE IF EXISTS "` + targetName + `"`).Error }()
	targetURL, err := withDatabase(base, targetName)
	if err != nil {
		t.Fatal(err)
	}
	target, err := Open(targetURL)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(target)

	dir := t.TempDir()
	stage := 1
	regs := []Registry{Of(liveRegistry{&stage}), Of(liveOwners{&stage})}
	cfg := Config{
		URL:          targetURL,
		ScratchURL:   base,
		Dir:          dir,
		Header:       "-- Copyright 2026 Test.\n-- SPDX-License-Identifier: Apache-2.0",
		AtlasVersion: AtlasVersion,
	}
	run := func(args ...string) error {
		t.Helper()
		return Run(ctx, cfg, args, regs...)
	}
	must := func(args ...string) {
		t.Helper()
		if err := run(args...); err != nil {
			t.Fatalf("%s: %v", strings.Join(args, " "), err)
		}
	}
	// files lists the migrations, and the next diff waits out the second so
	// atlas's file name, and the ones beside it, cannot collide.
	files := func() []string {
		t.Helper()
		got, err := sqlFiles(dir)
		if err != nil {
			t.Fatal(err)
		}
		for i := range got {
			got[i] = filepath.Base(got[i])
		}
		time.Sleep(1100 * time.Millisecond)
		return got
	}
	read := func(suffix string) string {
		t.Helper()
		got, _ := sqlFiles(dir)
		for _, f := range got {
			if strings.HasSuffix(f, suffix) {
				src, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				return string(src)
			}
		}
		t.Fatalf("no migration ending %s in %v", suffix, got)
		return ""
	}

	// Stage 1: diff writes the extension and the table, apply runs them,
	// check is clean and a second diff writes nothing.
	must("diff", "init")
	if got := files(); len(got) != 2 || !strings.HasSuffix(got[0], "_extensions.sql") || !strings.HasSuffix(got[1], "_init.sql") {
		t.Fatalf("stage 1 wrote %v, want extensions + init", got)
	}
	if src := read("_extensions.sql"); !strings.HasPrefix(src, "-- Copyright 2026 Test.\n") || !strings.Contains(src, `CREATE EXTENSION IF NOT EXISTS "pg_trgm";`) {
		t.Errorf("extensions migration:\n%s", src)
	}
	if src := read("_init.sql"); !strings.HasPrefix(src, "-- Copyright 2026 Test.\n") || !strings.Contains(src, `CREATE TABLE "app"."widgets"`) {
		t.Errorf("init migration:\n%s", src)
	}
	must("apply")
	must("check")
	must("diff", "again")
	if got := files(); len(got) != 2 {
		t.Fatalf("an unchanged diff wrote %v", got)
	}

	// Stage 2: a function and a trigger. Atlas's community build ignores
	// both, so check fails, and diff writes them as a routines migration.
	stage = 2
	if err := run("check"); err == nil || !strings.Contains(err.Error(), "has no migration") {
		t.Fatalf("check after adding a trigger = %v, want a missing-migration error", err)
	}
	files()
	must("diff", "lock")
	if got := files(); len(got) != 3 || !strings.HasSuffix(got[2], "_lock_routines.sql") {
		t.Fatalf("stage 2 wrote %v, want a lock_routines migration", got)
	}
	routines := read("_lock_routines.sql")
	for _, want := range []string{
		`CREATE OR REPLACE FUNCTION app.widgets_lock()`,
		`DROP TRIGGER IF EXISTS "widgets_lock" ON app.widgets;`,
		`CREATE TRIGGER widgets_lock BEFORE INSERT OR UPDATE OF name ON app.widgets FOR EACH ROW EXECUTE FUNCTION app.widgets_lock();`,
	} {
		if !strings.Contains(routines, want) {
			t.Errorf("routines migration lacks %q:\n%s", want, routines)
		}
	}
	must("apply")
	must("check")
	var triggers int64
	if err := target.Raw(`SELECT count(*) FROM pg_trigger WHERE tgname = 'widgets_lock'`).Scan(&triggers).Error; err != nil || triggers != 1 {
		t.Fatalf("applied trigger count = %d, %v", triggers, err)
	}

	// Stage 3: a second registry and a foreign key into it, added NOT
	// VALID, so it applies over a row that breaks it.
	stage = 3
	if err := target.Exec(`INSERT INTO "app"."widgets" (id, name, owner) VALUES (1, 'dangling', 42)`).Error; err != nil {
		t.Fatal(err)
	}
	must("diff", "owners")
	got := files()
	if len(got) != 4 || !strings.HasSuffix(got[3], "_owners.sql") {
		t.Fatalf("stage 3 wrote %v, want an owners migration", got)
	}
	if src := read("_owners.sql"); !strings.Contains(src, `ADD CONSTRAINT "fk_widgets_owner" FOREIGN KEY ("owner") REFERENCES "ext"."owners" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT VALID;`) {
		t.Errorf("owners migration does not add the key NOT VALID:\n%s", src)
	}
	must("apply")
	must("check")

	// validate writes the guarded VALIDATE CONSTRAINT once, and nothing the
	// second time; it applies once the dangling row is gone.
	must("validate")
	if got := files(); len(got) != 5 || !strings.HasSuffix(got[4], "_validate_foreign_keys.sql") {
		t.Fatalf("validate wrote %v", got)
	}
	if src := read("_validate_foreign_keys.sql"); !strings.Contains(src, `ALTER TABLE "app"."widgets" VALIDATE CONSTRAINT "fk_widgets_owner";`) {
		t.Errorf("validate migration:\n%s", src)
	}
	must("validate")
	if got := files(); len(got) != 5 {
		t.Fatalf("a second validate wrote %v", got)
	}
	if err := run("apply"); err == nil {
		t.Fatal("applying the validation over a dangling row succeeded")
	}
	if err := target.Exec(`DELETE FROM "app"."widgets" WHERE id = 1`).Error; err != nil {
		t.Fatal(err)
	}
	must("apply")
	must("check")
	var valid bool
	if err := target.Raw(`SELECT convalidated FROM pg_constraint WHERE conname = 'fk_widgets_owner'`).Scan(&valid).Error; err != nil || !valid {
		t.Fatalf("fk_widgets_owner validated = %v, %v", valid, err)
	}

	// The directory's sum is atlas's own: it still validates.
	cmd := exec.Command("atlas", "migrate", "validate", "--dir", "file://"+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("atlas migrate validate: %v\n%s", err, out)
	}
}
