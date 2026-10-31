// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package references_test

// joins_test.go checks the repeated references kept in join tables -- a
// shelf's featured books (a nested target, kept by name) and a book's shelves
// (a top-level target, kept by id) -- round-trip through Create, Get, List,
// Update and a Batch, and filter; that a parent with a "-" in it widens only
// its own segment; that Count and Facet agree with List; and that a record
// join rows name is kept by a purge, or refused by a delete, as a singular
// reference keeps or refuses it.

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/the-protobuf-project/store/examples/generated/go/gorm/references/filterx"
	"github.com/the-protobuf-project/store/examples/generated/go/protobuf/references/catalogv1"
	"github.com/the-protobuf-project/store/examples/generated/go/repository/references/catalog/catalog"
	"github.com/the-protobuf-project/store/examples/generated/go/repository/references/repox"
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

// TestTimestampFacetIsCanonical: a timestamp's facet value is RFC 3339 in
// UTC to the microsecond (repox.FacetTimeLayout) -- in SQL, and from the ISO
// forms a GraphQL engine returns.
func TestTimestampFacetIsCanonical(t *testing.T) {
	for in, want := range map[string]string{
		"2026-10-29T08:30:00+05:30":        "2026-10-29T03:00:00.000000Z",
		"2026-10-29T03:00:00.123456+00:00": "2026-10-29T03:00:00.123456Z",
		"2026-10-29T03:00:00.5Z":           "2026-10-29T03:00:00.500000Z",
		"2026-10-29 03:00:00+00":           "2026-10-29T03:00:00.000000Z",
	} {
		if got := repox.FacetValue(filterx.KindTimestamp, in); got != want {
			t.Errorf("FacetValue(%q) = %q, want %q", in, got, want)
		}
	}
	w := newWorld(t)
	ctx := context.Background()
	s, err := w.cat.Shelfs.Create(ctx, &catalogv1.Shelf{DisplayName: "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Shelfs.Delete(ctx, s.GetName()); err != nil {
		t.Fatal(err)
	}
	page, _, err := w.cat.Shelfs.List(ctx, repox.ListInput{ShowDeleted: true})
	if err != nil || len(page) != 1 {
		t.Fatalf("list deleted: %v %v", page, err)
	}
	buckets, err := w.cat.Shelfs.Facet(ctx, repox.ListInput{ShowDeleted: true}, "delete_time", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := page[0].GetDeleteTime().AsTime().UTC().Format(repox.FacetTimeLayout)
	if !slices.Equal(buckets, []repox.Bucket{{Value: want, Count: 1}}) {
		t.Fatalf("facet delete_time: %v, want %s", buckets, want)
	}
}

// TestPurgeKeepsJoinTargets: a record a join table's rows name -- a shelf a
// book is filed under, a book a shelf features -- is a referrer's target: a
// purge keeps it, and the field keeps naming it, until the owner lets it go.
func TestPurgeKeepsJoinTargets(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	_, book := w.book(t)
	target, err := w.cat.Shelfs.Create(ctx, &catalogv1.Shelf{DisplayName: "classics"})
	if err != nil {
		t.Fatal(err)
	}
	if book, err = w.cat.Books.Update(ctx, &catalogv1.Book{Name: book.GetName(), FiledUnder: []string{target.GetName()}}, []string{"filed_under"}); err != nil {
		t.Fatal(err)
	}
	picks, err := w.cat.Shelfs.Create(ctx, &catalogv1.Shelf{DisplayName: "picks", Featured: []string{book.GetName()}})
	if err != nil {
		t.Fatal(err)
	}

	for _, purge := range []func() error{
		func() error { return w.cat.Shelfs.Purge(ctx, target.GetName()) },
		func() error {
			b := catalog.NewBatch(w.conn)
			b.PurgeShelf(target.GetName())
			return b.Commit(ctx)
		},
	} {
		if err := purge(); err != nil {
			t.Fatalf("a purge never fails because of a referrer: %v", err)
		}
		if _, err := w.cat.Shelfs.Get(ctx, target.GetName()); err != nil {
			t.Fatalf("shelf purged from under the book filed under it: %v", err)
		}
		if got, err := w.cat.Books.Get(ctx, book.GetName()); err != nil || !slices.Equal(got.GetFiledUnder(), []string{target.GetName()}) {
			t.Fatalf("book after the purge: filed_under %v %v, want [%s]", got.GetFiledUnder(), err, target.GetName())
		}
	}
	if err := w.cat.Books.Delete(ctx, book.GetName()); err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Books.Purge(ctx, book.GetName()); err != nil {
		t.Fatal(err)
	}
	if got, err := w.cat.Shelfs.Get(ctx, picks.GetName()); err != nil || !slices.Equal(got.GetFeatured(), []string{book.GetName()}) {
		t.Fatalf("book purged from under the shelf featuring it: %v %v", got.GetFeatured(), err)
	}
	want := []string{target.GetName() + ":foreign key", target.GetName() + ":foreign key", book.GetName() + ":foreign key"}
	if !slices.Equal(w.kept, want) {
		t.Fatalf("kept %v, want %v", w.kept, want)
	}

	// Once the owners let go, the purge goes ahead.
	if err := w.cat.Shelfs.Purge(ctx, picks.GetName()); err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Books.Purge(ctx, book.GetName()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cat.Books.Get(ctx, book.GetName()); !errors.Is(err, repox.ErrNotFound) {
		t.Fatalf("book not purged once nothing names it: %v", err)
	}
	if err := w.cat.Shelfs.Purge(ctx, target.GetName()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cat.Shelfs.Get(ctx, target.GetName()); !errors.Is(err, repox.ErrNotFound) {
		t.Fatalf("shelf not purged once nothing names it: %v", err)
	}
}

// TestDeleteRefusesJoinTargets: a record deleted outright (no soft delete)
// that a join table's rows name -- a genre a book is classed under -- is
// refused with ErrForeignKey, as a singular reference's constraint refuses
// it, rather than cascaded out of the books; once the book lets go it goes.
func TestDeleteRefusesJoinTargets(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	_, book := w.book(t)
	g, err := w.cat.Genres.Create(ctx, &catalogv1.Genre{DisplayName: "Science fiction"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.cat.Books.Update(ctx, &catalogv1.Book{Name: book.GetName(), Genres: []string{g.GetName()}}, []string{"genres"}); err != nil {
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
			t.Fatalf("delete of a genre a book names: %v, want ErrForeignKey naming %s", err, g.GetName())
		}
		if got, err := w.cat.Books.Get(ctx, book.GetName()); err != nil || !slices.Equal(got.GetGenres(), []string{g.GetName()}) {
			t.Fatalf("book after the refused delete: genres %v %v", got.GetGenres(), err)
		}
	}
	if _, err := w.cat.Books.Update(ctx, &catalogv1.Book{Name: book.GetName()}, []string{"genres"}); err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Genres.Delete(ctx, g.GetName()); err != nil {
		t.Fatalf("delete once nothing names it: %v", err)
	}
	if _, err := w.cat.Genres.Get(ctx, g.GetName()); !errors.Is(err, repox.ErrNotFound) {
		t.Fatalf("genre not deleted: %v", err)
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
