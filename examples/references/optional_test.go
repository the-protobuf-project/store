// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package references_test

// optional_test.go checks the optional singular references of a book: one
// that takes no action (primary_genre, returns_to) refuses a delete and keeps
// a purged record while it names it, as a required reference and a join
// table's row do, even on a database whose constraint still sets null; one
// that says SET_NULL (former_genre) is blanked by the delete.

import (
	"context"
	"errors"
	"testing"

	"github.com/the-protobuf-project/store/examples/generated/go/protobuf/references/catalogv1"
	"github.com/the-protobuf-project/store/examples/generated/go/repository/references/catalog/catalog"
	"github.com/the-protobuf-project/store/examples/generated/go/repository/references/repox"
)

// TestDeleteRefusesOptionalReferences: a genre a book is shelved by is
// refused with ErrForeignKey by Delete and by a Batch -- also once the
// constraint is the SET NULL an older migration wrote -- and goes once the
// book lets go of it.
func TestDeleteRefusesOptionalReferences(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	_, book := w.book(t)
	g, err := w.cat.Genres.Create(ctx, &catalogv1.Genre{DisplayName: "Science fiction"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.cat.Books.Update(ctx, &catalogv1.Book{Name: book.GetName(), PrimaryGenre: g.GetName()}, []string{"primary_genre"}); err != nil {
		t.Fatal(err)
	}
	// What a database migrated before the default took no action still has:
	// its every constraint on the column replaced by one that sets null.
	if err := w.conn.Gorm.Exec(`DO $$
DECLARE c record;
BEGIN
	FOR c IN SELECT conname FROM pg_constraint
		WHERE conrelid = '"catalog"."books"'::regclass AND contype = 'f'
		AND conkey = ARRAY[(SELECT attnum FROM pg_attribute
			WHERE attrelid = '"catalog"."books"'::regclass AND attname = 'primary_genre')]
	LOOP
		EXECUTE format('ALTER TABLE "catalog"."books" DROP CONSTRAINT %I', c.conname);
	END LOOP;
	ALTER TABLE "catalog"."books" ADD CONSTRAINT "fk_books_primary_genre"
		FOREIGN KEY ("primary_genre") REFERENCES "catalog"."genres"("id") ON DELETE SET NULL;
END $$`).Error; err != nil {
		t.Fatal(err)
	}
	for _, del := range []func() error{
		func() error { return w.cat.Genres.Delete(ctx, g.GetName()) },
		func() error {
			b := catalog.NewBatch(w.conn)
			b.DeleteGenre(g.GetName())
			return b.Commit(ctx)
		},
	} {
		err := del()
		var re *repox.ReferenceError
		if !errors.Is(err, repox.ErrForeignKey) || !errors.As(err, &re) || re.Name != g.GetName() {
			t.Fatalf("delete of a genre a book is shelved by: %v, want ErrForeignKey naming %s", err, g.GetName())
		}
		if got, err := w.cat.Books.Get(ctx, book.GetName()); err != nil || got.GetPrimaryGenre() != g.GetName() {
			t.Fatalf("book after the refused delete: primary_genre %q %v", got.GetPrimaryGenre(), err)
		}
	}
	if _, err := w.cat.Books.Update(ctx, &catalogv1.Book{Name: book.GetName()}, []string{"primary_genre"}); err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Genres.Delete(ctx, g.GetName()); err != nil {
		t.Fatalf("delete once nothing names it: %v", err)
	}
}

// TestDeleteBlanksSetNullReferences: an optional reference that says
// SET_NULL does not refuse the delete; the database blanks it.
func TestDeleteBlanksSetNullReferences(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	_, book := w.book(t)
	g, err := w.cat.Genres.Create(ctx, &catalogv1.Genre{DisplayName: "Westerns"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.cat.Books.Update(ctx, &catalogv1.Book{Name: book.GetName(), FormerGenre: g.GetName()}, []string{"former_genre"}); err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Genres.Delete(ctx, g.GetName()); err != nil {
		t.Fatalf("delete of a genre a SET_NULL reference names: %v", err)
	}
	if got, err := w.cat.Books.Get(ctx, book.GetName()); err != nil || got.GetFormerGenre() != "" {
		t.Fatalf("book after the delete: former_genre %q %v, want blank", got.GetFormerGenre(), err)
	}
}

// TestPurgeKeepsOptionalReferenceTargets: a deleted shelf a book returns to
// is kept by Purge, and goes once the book lets go of it.
func TestPurgeKeepsOptionalReferenceTargets(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	_, book := w.book(t)
	target, err := w.cat.Shelfs.Create(ctx, &catalogv1.Shelf{DisplayName: "returns"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.cat.Books.Update(ctx, &catalogv1.Book{Name: book.GetName(), ReturnsTo: target.GetName()}, []string{"returns_to"}); err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Shelfs.Delete(ctx, target.GetName()); err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Shelfs.Purge(ctx, target.GetName()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cat.Shelfs.Get(ctx, target.GetName()); err != nil {
		t.Fatalf("shelf a book returns to purged: %v", err)
	}
	if got, err := w.cat.Books.Get(ctx, book.GetName()); err != nil || got.GetReturnsTo() != target.GetName() {
		t.Fatalf("book after the kept purge: returns_to %q %v", got.GetReturnsTo(), err)
	}
	if _, err := w.cat.Books.Update(ctx, &catalogv1.Book{Name: book.GetName()}, []string{"returns_to"}); err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Shelfs.Purge(ctx, target.GetName()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cat.Shelfs.Get(ctx, target.GetName()); !errors.Is(err, repox.ErrNotFound) {
		t.Fatalf("shelf not purged once nothing names it: %v", err)
	}
}
