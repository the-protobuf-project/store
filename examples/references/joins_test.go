// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package references_test

// joins_test.go checks the repeated references kept in join tables -- a
// shelf's featured books (a nested target, kept by name) and a book's shelves
// (a top-level target, kept by id) -- round-trip through Create, Get, List,
// Update and a Batch, and filter; that a parent with a "-" in it widens only
// its own segment; and that Count and Facet agree with List.

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/the-protobuf-project/store/examples/references/gen/pb/catalogv1"
	"github.com/the-protobuf-project/store/examples/references/gen/repository/catalog/catalog"
	"github.com/the-protobuf-project/store/examples/references/gen/repository/repox"
)

func TestJoinFieldsRoundTrip(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	a, b := twoShelves(t, w)
	duneA, duneB := a.GetName()+"/books/dune", b.GetName()+"/books/dune"

	// A nested target, kept by name, in the order given; a repeat keeps its
	// first place.
	s, err := w.cat.Shelfs.Create(ctx, &catalogv1.Shelf{DisplayName: "staff picks", Featured: []string{duneB, duneA, duneB}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{duneB, duneA}; !slices.Equal(s.GetFeatured(), want) {
		t.Fatalf("create: featured %v, want %v", s.GetFeatured(), want)
	}
	got, err := w.cat.Shelfs.Get(ctx, s.GetName())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{duneB, duneA}; !slices.Equal(got.GetFeatured(), want) {
		t.Fatalf("get: featured %v, want %v", got.GetFeatured(), want)
	}

	// An update outside the mask keeps the rows; one inside replaces them.
	got.DisplayName = "picks"
	if got, err = w.cat.Shelfs.Update(ctx, got, []string{"display_name"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{duneB, duneA}; !slices.Equal(got.GetFeatured(), want) {
		t.Fatalf("update display_name: featured %v, want %v", got.GetFeatured(), want)
	}
	got.Featured = []string{duneA}
	if got, err = w.cat.Shelfs.Update(ctx, got, []string{"featured"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{duneA}; !slices.Equal(got.GetFeatured(), want) {
		t.Fatalf("update featured: featured %v, want %v", got.GetFeatured(), want)
	}

	// A name no record holds is refused, as a foreign key would be.
	if _, err := w.cat.Shelfs.Create(ctx, &catalogv1.Shelf{DisplayName: "x", Featured: []string{a.GetName() + "/books/nope"}}); !errors.Is(err, repox.ErrForeignKey) {
		t.Fatalf("create with a missing book: %v, want ErrForeignKey", err)
	}

	// A top-level target, kept by id: the name is rebuilt from it.
	book, err := w.cat.Books.Create(ctx, a.GetName(), &catalogv1.Book{Title: "Emma", FiledUnder: []string{b.GetName(), s.GetName()}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{b.GetName(), s.GetName()}; !slices.Equal(book.GetFiledUnder(), want) {
		t.Fatalf("book: filed_under %v, want %v", book.GetFiledUnder(), want)
	}

	// List reads the rows back, and filters on them.
	page, _, err := w.cat.Shelfs.List(ctx, repox.ListInput{Filter: `featured:"` + duneA + `"`})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].GetName() != s.GetName() || !slices.Equal(page[0].GetFeatured(), []string{duneA}) {
		t.Fatalf("list featured:%s: %v", duneA, page)
	}
	if page, _, err = w.cat.Shelfs.List(ctx, repox.ListInput{Filter: `featured:*`}); err != nil || len(page) != 1 {
		t.Fatalf("list featured:*: %v %v", page, err)
	}
	books, _, err := w.cat.Books.List(ctx, "shelves/-", repox.ListInput{Filter: `filed_under="` + b.GetName() + `"`})
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 || books[0].GetName() != book.GetName() {
		t.Fatalf("list filed_under=%s: %v", b.GetName(), books)
	}

	// A Batch writes them too.
	batch := catalog.NewBatch(w.conn)
	name, err := batch.CreateShelf(&catalogv1.Shelf{DisplayName: "batched", Featured: []string{duneA, duneB}})
	if err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err = w.cat.Shelfs.Get(ctx, name); err != nil || !slices.Equal(got.GetFeatured(), []string{duneA, duneB}) {
		t.Fatalf("batched shelf: %v %v", got.GetFeatured(), err)
	}

	// A shelf whose rows refer to it is still purged: they are its own.
	if err := w.cat.Shelfs.Purge(ctx, got.GetName()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cat.Shelfs.Get(ctx, got.GetName()); !errors.Is(err, repox.ErrNotFound) {
		t.Fatalf("purged shelf: %v, want ErrNotFound (kept %v)", err, w.kept)
	}
}

// TestWildcardParentKeepsNamedSegments: "shelves/{a}/books/-" reads the
// pages of shelf a's books only, and "shelves/-/books/-" every page.
func TestWildcardParentKeepsNamedSegments(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	a, _ := twoShelves(t, w)
	pages, _, err := w.cat.Pages.List(ctx, a.GetName()+"/books/-", repox.ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pages[0].GetName() != a.GetName()+"/books/dune/pages/p1" {
		t.Fatalf("pages under %s/books/-: %v", a.GetName(), pages)
	}
	if n, err := w.cat.Pages.Count(ctx, a.GetName()+"/books/-", repox.ListInput{}); err != nil || n != 1 {
		t.Fatalf("count under %s/books/-: %d %v", a.GetName(), n, err)
	}
	if n, err := w.cat.Pages.Count(ctx, "shelves/-/books/-", repox.ListInput{}); err != nil || n != 2 {
		t.Fatalf("count under shelves/-/books/-: %d %v", n, err)
	}
}

// TestCountAndFacet: Count matches List, and Facet groups by a column and
// by a join-table field.
func TestCountAndFacet(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	a, b := twoShelves(t, w)
	duneA := a.GetName() + "/books/dune"
	for _, f := range [][]string{{duneA}, {duneA}, nil} {
		if _, err := w.cat.Shelfs.Create(ctx, &catalogv1.Shelf{DisplayName: "picks", Featured: f}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := w.cat.Shelfs.Count(ctx, repox.ListInput{}); err != nil || n != 5 {
		t.Fatalf("count: %d %v, want 5", n, err)
	}
	if n, err := w.cat.Shelfs.Count(ctx, repox.ListInput{Filter: `display_name="picks"`}); err != nil || n != 3 {
		t.Fatalf("count picks: %d %v, want 3", n, err)
	}
	buckets, err := w.cat.Shelfs.Facet(ctx, repox.ListInput{}, "display_name", 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []repox.Bucket{{Value: "picks", Count: 3}, {Value: "shelf", Count: 2}}; !slices.Equal(buckets, want) {
		t.Fatalf("facet display_name: %v, want %v", buckets, want)
	}
	if buckets, err = w.cat.Shelfs.Facet(ctx, repox.ListInput{}, "featured", 1); err != nil {
		t.Fatal(err)
	}
	if want := []repox.Bucket{{Value: duneA, Count: 2}}; !slices.Equal(buckets, want) {
		t.Fatalf("facet featured: %v, want %v", buckets, want)
	}
	if n, err := w.cat.Books.Count(ctx, b.GetName(), repox.ListInput{}); err != nil || n != 1 {
		t.Fatalf("books under %s: %d %v", b.GetName(), n, err)
	}
	if _, err := w.cat.Shelfs.Facet(ctx, repox.ListInput{}, "nope", 0); !errors.Is(err, repox.ErrInvalidArgument) {
		t.Fatalf("facet by an unknown field: %v, want ErrInvalidArgument", err)
	}
}

// TestStoreDecorator: WithShelfStore puts a decorator under the repository,
// which a write and a one-row read go through, on its own handle and inside
// a Batch's transaction.
func TestStoreDecorator(t *testing.T) {
	gets, built := 0, 0
	w := newWorld(t, catalog.WithShelfStore(countingShelfStore(&gets, &built)))
	ctx := context.Background()
	s, err := w.cat.Shelfs.Create(ctx, &catalogv1.Shelf{DisplayName: "decorated"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.cat.Shelfs.Get(ctx, s.GetName()); err != nil {
		t.Fatal(err)
	}
	if gets < 2 || built < 2 {
		t.Fatalf("decorator: %d reads through it, %d stores built; want the create's re-read and the get", gets, built)
	}
	before := built
	var batchGets int
	batch := catalog.NewBatch(w.conn, catalog.WithShelfStore(countingShelfStore(&batchGets, &built)))
	if _, err := batch.CreateShelf(&catalogv1.Shelf{DisplayName: "batched"}); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if built == before {
		t.Fatal("a Batch's create did not go through the decorator")
	}
}
