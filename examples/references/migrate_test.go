// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package references_test

// migrate_test.go checks the cross-database foreign key both ways it is
// installed: the SQL target's migrate.sql and validate.sql, and the GORM
// registries' MigrateExternal and ValidateExternal. Both must be idempotent,
// and both must cope with the other database not being there yet.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/the-protobuf-project/store/examples/generated/go/gorm/references/catalog"
	"github.com/the-protobuf-project/store/examples/generated/go/gorm/references/lending"
	"gorm.io/gorm"
)

// migrateAll migrates both databases with GORM, external keys last.
func migrateAll(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, step := range []func(*gorm.DB) error{
		catalog.Default.EnsureSchemas, lending.Default.EnsureSchemas,
		func(db *gorm.DB) error { return catalog.Default.Migrate(db) },
		func(db *gorm.DB) error { return lending.Default.Migrate(db) },
		catalog.Default.MigrateExternal, lending.Default.MigrateExternal,
	} {
		if err := step(db); err != nil {
			t.Fatal(err)
		}
	}
}

// constraint reports whether fk_loans_book exists and is validated.
func constraint(t *testing.T, db *gorm.DB) (exists, valid bool) {
	t.Helper()
	var rows []struct{ Convalidated bool }
	if err := db.Raw(`SELECT convalidated FROM pg_constraint WHERE conname = 'fk_loans_book'`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		return false, false
	}
	return true, rows[0].Convalidated
}

func runSQL(t *testing.T, db *gorm.DB, file string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "generated", "sql", "references", file))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(b)).Error; err != nil {
		t.Fatalf("%s: %v", file, err)
	}
}

func TestSQLForeignKeyDDLIsIdempotent(t *testing.T) {
	db := freshDB(t)
	// The referring database first: its target does not exist yet, so the
	// constraint is skipped, not failed.
	runSQL(t, db, "lending/migrate.sql")
	if ok, _ := constraint(t, db); ok {
		t.Fatal("constraint added before its target exists")
	}
	runSQL(t, db, "catalog/migrate.sql")
	runSQL(t, db, "lending/migrate.sql") // now it lands
	if ok, valid := constraint(t, db); !ok || valid {
		t.Fatalf("after migrate: exists=%v valid=%v, want added NOT VALID", ok, valid)
	}
	runSQL(t, db, "lending/migrate.sql") // and again: no change, no error
	runSQL(t, db, "catalog/migrate.sql")
	runSQL(t, db, "lending/validate.sql")
	if _, valid := constraint(t, db); !valid {
		t.Fatal("validate.sql left it NOT VALID")
	}
	runSQL(t, db, "lending/validate.sql")
	var n int64
	db.Raw(`SELECT count(*) FROM pg_constraint WHERE conname = 'fk_loans_book'`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d constraints, want 1", n)
	}
}

func TestGormMigrateExternalIsIdempotent(t *testing.T) {
	db := freshDB(t)
	if err := lending.Default.EnsureSchemas(db); err != nil {
		t.Fatal(err)
	}
	if err := lending.Default.Migrate(db); err != nil {
		t.Fatal(err)
	}
	// The catalog is not migrated yet: skipped with a log line.
	if err := lending.Default.MigrateExternal(db); err != nil {
		t.Fatal(err)
	}
	if ok, _ := constraint(t, db); ok {
		t.Fatal("constraint added before its target exists")
	}
	migrateAll(t, db)
	migrateAll(t, db)
	if ok, valid := constraint(t, db); !ok || valid {
		t.Fatalf("exists=%v valid=%v, want added NOT VALID", ok, valid)
	}
	// A row that predates the constraint and names no book: VALIDATE fails
	// on it, and passes once it is fixed.
	if err := db.Exec(`ALTER TABLE lending.loans DROP CONSTRAINT fk_loans_book`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO lending.loans (id, name, book, borrower) VALUES ('L1', 'loans/L1', 'shelves/x/books/missing', 'cards/1')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := lending.Default.MigrateExternal(db); err != nil {
		t.Fatalf("NOT VALID must accept existing rows: %v", err)
	}
	if err := lending.Default.ValidateExternal(db); err == nil {
		t.Fatal("ValidateExternal accepted a dangling row")
	}
	db.Exec(`DELETE FROM lending.loans`)
	if err := lending.Default.ValidateExternal(db); err != nil {
		t.Fatal(err)
	}
	if _, valid := constraint(t, db); !valid {
		t.Fatal("still NOT VALID")
	}
	if err := lending.Default.ValidateExternal(db); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Default.ValidateExternal(db); err != nil {
		t.Fatal(err)
	}
}
