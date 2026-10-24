// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package references_test

// nested_test.go checks that a nested resource's id is unique only under its
// parent: two shelves can each hold a book "dune", and every generated path
// -- Create, Get, List, Update, UpdateOutputOnly, Delete, Undelete, Purge,
// PurgeExpired, the Batch writes, and a page two levels down -- addresses
// the one it names. A row stored before rows were keyed by name (its key
// the bare id) stays readable and writable.

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/the-protobuf-project/store/examples/references/gen/pb/catalogv1"
	"github.com/the-protobuf-project/store/examples/references/gen/repository/catalog/catalog"
	"github.com/the-protobuf-project/store/examples/references/gen/repository/repox"
)

// twoShelves creates two shelves, each with a book "dune" and a page "p1" in
// it.
func twoShelves(t *testing.T, w *world) (a, b *catalogv1.Shelf) {
	t.Helper()
	ctx := context.Background()
	for _, s := range []**catalogv1.Shelf{&a, &b} {
		shelf, err := w.cat.Shelfs.Create(ctx, &catalogv1.Shelf{DisplayName: "shelf"})
		if err != nil {
			t.Fatal(err)
		}
		*s = shelf
		book, err := w.cat.Books.Create(ctx, shelf.GetName(), &catalogv1.Book{Name: shelf.GetName() + "/books/dune", Title: "Dune on " + shelf.GetName()})
		if err != nil {
			t.Fatalf("create %s/books/dune: %v", shelf.GetName(), err)
		}
		if _, err := w.cat.Pages.Create(ctx, book.GetName(), &catalogv1.Page{Name: book.GetName() + "/pages/p1", Text: "page of " + book.GetName()}); err != nil {
			t.Fatalf("create %s/pages/p1: %v", book.GetName(), err)
		}
	}
	return a, b
}

func title(t *testing.T, w *world, name string) string {
	t.Helper()
	b, err := w.cat.Books.Get(context.Background(), name)
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	if b.GetName() != name {
		t.Fatalf("get %s returned %s", name, b.GetName())
	}
	return b.GetTitle()
}

func TestSameChildIDUnderTwoParents(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a, b := twoShelves(t, w)
	da, db := a.GetName()+"/books/dune", b.GetName()+"/books/dune"

	if title(t, w, da) != "Dune on "+a.GetName() || title(t, w, db) != "Dune on "+b.GetName() {
		t.Fatal("each name must read its own book")
	}
	if _, err := w.cat.Books.Create(ctx, a.GetName(), &catalogv1.Book{Name: da, Title: "again"}); !errors.Is(err, repox.ErrAlreadyExists) {
		t.Fatalf("a second dune on one shelf: %v, want ErrAlreadyExists", err)
	}
	if _, err := w.cat.Books.Get(ctx, a.GetName()+"/books/missing"); !errors.Is(err, repox.ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}

	// List scopes to the parent.
	for _, s := range []*catalogv1.Shelf{a, b} {
		rows, _, err := w.cat.Books.List(ctx, s.GetName(), repox.ListInput{PageSize: 10})
		if err != nil || len(rows) != 1 || rows[0].GetName() != s.GetName()+"/books/dune" {
			t.Fatalf("list %s: %v %v", s.GetName(), rows, err)
		}
	}

	// Update and UpdateOutputOnly touch only the named one.
	ba, _ := w.cat.Books.Get(ctx, da)
	ba.Title = "Dune, revised"
	if _, err := w.cat.Books.Update(ctx, ba, []string{"title"}); err != nil {
		t.Fatal(err)
	}
	if title(t, w, da) != "Dune, revised" || title(t, w, db) != "Dune on "+b.GetName() {
		t.Fatal("update reached the wrong book")
	}
	exp := timestamppb.New(time.Now().Add(time.Hour).UTC().Truncate(time.Second))
	if _, err := w.cat.Books.UpdateOutputOnly(ctx, &catalogv1.Book{Name: db, ExpireTime: exp}, []string{"expire_time"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.cat.Books.Get(ctx, da); got.GetExpireTime() != nil {
		t.Fatal("UpdateOutputOnly reached the wrong book")
	}

	// Delete, Undelete, Purge.
	if err := w.cat.Books.Delete(ctx, da); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.cat.Books.Get(ctx, db); got.GetDeleteTime() != nil {
		t.Fatal("delete reached the wrong book")
	}
	if _, err := w.cat.Books.Undelete(ctx, db); !errors.Is(err, repox.ErrAlreadyExists) {
		t.Fatalf("undelete of the live one: %v", err)
	}
	if _, err := w.cat.Books.Undelete(ctx, da); err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Pages.Delete(ctx, da+"/pages/p1"); err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Books.Purge(ctx, da); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cat.Books.Get(ctx, da); !errors.Is(err, repox.ErrNotFound) {
		t.Fatalf("purged: %v", err)
	}
	if title(t, w, db) != "Dune on "+b.GetName() {
		t.Fatal("purge reached the wrong book")
	}
}

func TestNestedTwoDeep(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a, b := twoShelves(t, w)
	for _, s := range []*catalogv1.Shelf{a, b} {
		name := s.GetName() + "/books/dune/pages/p1"
		p, err := w.cat.Pages.Get(ctx, name)
		if err != nil || p.GetText() != "page of "+s.GetName()+"/books/dune" {
			t.Fatalf("get %s: %v %v", name, p, err)
		}
		rows, _, err := w.cat.Pages.List(ctx, s.GetName()+"/books/dune", repox.ListInput{PageSize: 10})
		if err != nil || len(rows) != 1 || rows[0].GetName() != name {
			t.Fatalf("list under %s: %v %v", s.GetName(), rows, err)
		}
	}
	// "-" for the shelf reads the dune of every shelf.
	rows, _, err := w.cat.Pages.List(ctx, "shelves/-/books/dune", repox.ListInput{PageSize: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("list under shelves/-/books/dune: %v %v", rows, err)
	}
	// A page's book column points at its own book's row.
	var mismatched int64
	if err := w.conn.Gorm.Raw(`SELECT count(*) FROM catalog.pages p JOIN catalog.books b ON b.id = p.book_id WHERE p.name NOT LIKE b.name || '/pages/%'`).Scan(&mismatched).Error; err != nil || mismatched != 0 {
		t.Fatalf("pages under the wrong book: %d, %v", mismatched, err)
	}
	// Under a book that does not exist, the foreign key refuses the page.
	if _, err := w.cat.Pages.Create(ctx, a.GetName()+"/books/nope", &catalogv1.Page{Text: "x"}); err == nil {
		t.Fatal("a page under a missing book was created")
	}
}

func TestNestedBatch(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a, b := twoShelves(t, w)
	da, db := a.GetName()+"/books/dune", b.GetName()+"/books/dune"

	batch := catalog.NewBatch(w.conn)
	named, err := batch.CreateBook(a.GetName(), &catalogv1.Book{Name: a.GetName() + "/books/emma", Title: "Emma"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := batch.CreatePage(named, &catalogv1.Page{Name: named + "/pages/p1", Text: "emma p1"}); err != nil {
		t.Fatal(err)
	}
	batch.UpdateBook(&catalogv1.Book{Name: db, Title: "Dune (batch)"}, []string{"title"})
	batch.DeleteBook(da)
	if err := batch.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if title(t, w, db) != "Dune (batch)" {
		t.Fatal("batch update reached the wrong book")
	}
	if got, _ := w.cat.Books.Get(ctx, da); got.GetDeleteTime() == nil {
		t.Fatal("batch delete missed")
	}
	if got, _ := w.cat.Books.Get(ctx, db); got.GetDeleteTime() != nil {
		t.Fatal("batch delete reached the wrong book")
	}
	if p, err := w.cat.Pages.Get(ctx, named+"/pages/p1"); err != nil || p.GetText() != "emma p1" {
		t.Fatalf("page created in the batch under a book of the batch: %v %v", p, err)
	}

	purge := catalog.NewBatch(w.conn)
	purge.DeletePage(da + "/pages/p1")
	purge.PurgeBook(da)
	if err := purge.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cat.Books.Get(ctx, da); !errors.Is(err, repox.ErrNotFound) {
		t.Fatalf("batch purge: %v", err)
	}
	if title(t, w, db) != "Dune (batch)" {
		t.Fatal("batch purge reached the wrong book")
	}
}

func TestNestedPurgeExpired(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, catalog.WithRetention(time.Millisecond))
	a, b := twoShelves(t, w)
	da, db := a.GetName()+"/books/dune", b.GetName()+"/books/dune"
	if err := w.cat.Pages.Delete(ctx, da+"/pages/p1"); err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Books.Delete(ctx, da); err != nil {
		t.Fatal(err)
	}
	res, err := w.cat.Books.PurgeExpired(ctx, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Purged) != 1 || res.Purged[0] != da {
		t.Fatalf("purged %v, want [%s]", res.Purged, da)
	}
	if title(t, w, db) != "Dune on "+b.GetName() {
		t.Fatal("PurgeExpired reached the wrong book")
	}
}

// A row written before nested rows were keyed by name has the bare id as its
// key; it is found by its name all the same.
func TestNestedLegacyRow(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	shelf, err := w.cat.Shelfs.Create(ctx, &catalogv1.Shelf{DisplayName: "old"})
	if err != nil {
		t.Fatal(err)
	}
	shelfID := shelf.GetName()[len("shelves/"):]
	name := shelf.GetName() + "/books/legacy"
	if err := w.conn.Gorm.Exec(`INSERT INTO catalog.books (id, name, title, etag, shelf_id) VALUES ('legacy', ?, 'Old', 'e0', ?)`, name, shelfID).Error; err != nil {
		t.Fatal(err)
	}
	if title(t, w, name) != "Old" {
		t.Fatal("legacy row unreadable")
	}
	if _, err := w.cat.Pages.Create(ctx, name, &catalogv1.Page{Name: name + "/pages/p1", Text: "old page"}); err != nil {
		t.Fatalf("page under a legacy book: %v", err)
	}
	rows, _, err := w.cat.Pages.List(ctx, name, repox.ListInput{PageSize: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("list under a legacy book: %v %v", rows, err)
	}
	if _, err := w.cat.Books.Update(ctx, &catalogv1.Book{Name: name, Title: "Old, revised"}, []string{"title"}); err != nil {
		t.Fatal(err)
	}
	if title(t, w, name) != "Old, revised" {
		t.Fatal("legacy row update lost")
	}
}
