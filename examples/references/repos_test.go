// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package references_test

// repos_test.go drives the generated GORM repositories against Postgres:
// writes resolve external references, foreign keys surface as
// repox.ErrForeignKey, deletes consult referrers, purges keep rows still
// referred to, UpdateWithOutputOnly writes server-owned fields under the
// etag guard, and the reconciler finds dangling references.

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/the-protobuf-project/store/examples/references/gen/pb/catalogv1"
	"github.com/the-protobuf-project/store/examples/references/gen/pb/lendingv1"
	"github.com/the-protobuf-project/store/examples/references/gen/repository/catalog/catalog"
	"github.com/the-protobuf-project/store/examples/references/gen/repository/lending/lending"
	"github.com/the-protobuf-project/store/examples/references/gen/repository/repox"
)

const bookType = "catalog.example.com/Book"

// world is one migrated database with both schemas' repositories over it.
type world struct {
	conn  repox.Conn
	refs  *repox.References
	cat   catalog.Repositories
	lend  lending.Repositories
	kept  []string
	cards cards
}

func newWorld(t *testing.T, opts ...catalog.Option) *world {
	t.Helper()
	db := freshDB(t)
	migrateAll(t, db)
	w := &world{cards: cards{"cards/1": true, "cards/2": true}}
	w.refs = repox.NewReferences().
		Resolver(cardType, repox.GetResolver(w.cards.get)).
		OnKept(func(_ context.Context, name, why string) { w.kept = append(w.kept, name+":"+why) })
	w.conn = repox.Conn{Gorm: db, References: w.refs}
	w.cat = catalog.New(w.conn, opts...)
	w.lend = lending.New(w.conn)
	return w
}

// book creates a shelf and a book on it.
func (w *world) book(t *testing.T) (*catalogv1.Shelf, *catalogv1.Book) {
	t.Helper()
	ctx := context.Background()
	s, err := w.cat.Shelfs.Create(ctx, &catalogv1.Shelf{DisplayName: "fiction"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.cat.Books.Create(ctx, s.GetName(), &catalogv1.Book{Title: "Dune"})
	if err != nil {
		t.Fatal(err)
	}
	return s, b
}

func countLoans(t *testing.T, w *world) int {
	t.Helper()
	rows, _, err := w.lend.Loans.List(context.Background(), repox.ListInput{PageSize: 100, ShowDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestWritesResolveReferences(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	_, book := w.book(t)

	loan, err := w.lend.Loans.Create(ctx, &lendingv1.Loan{Book: book.GetName(), Borrower: "cards/1"})
	if err != nil {
		t.Fatal(err)
	}

	// A card the card service does not know: refused before anything is written.
	_, err = w.lend.Loans.Create(ctx, &lendingv1.Loan{Book: book.GetName(), Borrower: "cards/404"})
	var re *repox.ReferenceError
	if !errors.Is(err, repox.ErrReferenceNotFound) || !errors.As(err, &re) || re.Field != "borrower" {
		t.Fatalf("missing card: %v", err)
	}
	// A book that does not exist: the cross-database foreign key refuses it.
	_, err = w.lend.Loans.Create(ctx, &lendingv1.Loan{Book: "shelves/x/books/404", Borrower: "cards/1"})
	if !errors.Is(err, repox.ErrForeignKey) || !errors.As(err, &re) || re.Constraint != "fk_loans_book" || re.Reason() != repox.ReasonForeignKey {
		t.Fatalf("missing book: %v", err)
	}
	if n := countLoans(t, w); n != 1 {
		t.Fatalf("%d loans, want 1", n)
	}

	// An update resolves what its mask sets...
	if _, err := w.lend.Loans.Update(ctx, &lendingv1.Loan{Name: loan.GetName(), Guarantor: "cards/404"}, []string{"guarantor"}); !errors.Is(err, repox.ErrReferenceNotFound) {
		t.Fatalf("update to a missing card: %v", err)
	}
	// ...and only that: a stale borrower outside the mask is not re-checked.
	delete(w.cards, "cards/1")
	if _, err := w.lend.Loans.Update(ctx, &lendingv1.Loan{Name: loan.GetName(), Borrower: "cards/1", Guarantor: "cards/2"}, []string{"guarantor"}); err != nil {
		t.Fatalf("update outside the stale field: %v", err)
	}
	w.cards["cards/1"] = true

	// A batch resolves before it commits: nothing lands.
	b := lending.NewBatch(w.conn)
	if _, err := b.CreateLoan(&lendingv1.Loan{Book: book.GetName(), Borrower: "cards/1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateLoan(&lendingv1.Loan{Book: book.GetName(), Borrower: "cards/404"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx); !errors.Is(err, repox.ErrReferenceNotFound) {
		t.Fatalf("batch: %v", err)
	}
	if n := countLoans(t, w); n != 1 {
		t.Fatalf("%d loans after a refused batch, want 1", n)
	}
}

func TestUpdateWithOutputOnly(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	_, book := w.book(t)
	loan, err := w.lend.Loans.Create(ctx, &lendingv1.Loan{Book: book.GetName(), Borrower: "cards/1"})
	if err != nil {
		t.Fatal(err)
	}

	// Plain Update ignores an OUTPUT_ONLY path (AIP-203).
	got, err := w.lend.Loans.Update(ctx, &lendingv1.Loan{Name: loan.GetName(), State: "LENT"}, []string{"state"})
	if err != nil || got.GetState() != "" {
		t.Fatalf("Update wrote state: %q %v", got.GetState(), err)
	}

	// UpdateWithOutputOnly writes both, in one guarded write.
	got, err = w.lend.Loans.UpdateWithOutputOnly(ctx, &lendingv1.Loan{Name: loan.GetName(), Etag: got.GetEtag(), Guarantor: "cards/2", State: "LENT"}, []string{"guarantor"}, []string{"state"})
	if err != nil || got.GetState() != "LENT" || got.GetGuarantor() != "cards/2" {
		t.Fatalf("UpdateWithOutputOnly: %v %v", got, err)
	}
	// A stale etag fails it whole.
	if _, err := w.lend.Loans.UpdateWithOutputOnly(ctx, &lendingv1.Loan{Name: loan.GetName(), Etag: loan.GetEtag(), Guarantor: "cards/1", State: "RETURNED"}, []string{"guarantor"}, []string{"state"}); !errors.Is(err, repox.ErrConflict) {
		t.Fatalf("stale etag: %v", err)
	}
	// It still resolves references in the caller's mask.
	if _, err := w.lend.Loans.UpdateWithOutputOnly(ctx, &lendingv1.Loan{Name: loan.GetName(), Guarantor: "cards/404"}, []string{"guarantor"}, []string{"state"}); !errors.Is(err, repox.ErrReferenceNotFound) {
		t.Fatalf("unresolved reference: %v", err)
	}

	// A BeforeUpdate hook's OUTPUT_ONLY field is written when outputPaths names it.
	hooked := lending.New(w.conn, lending.WithLoanHooks(lending.LoanHooks{
		BeforeUpdate: func(_ context.Context, _, merged *lendingv1.Loan, _ []string) error {
			merged.State = "HOOKED"
			return nil
		},
	}))
	got, err = hooked.Loans.UpdateWithOutputOnly(ctx, &lendingv1.Loan{Name: loan.GetName(), Guarantor: "cards/1"}, []string{"guarantor"}, []string{"state"})
	if err != nil || got.GetState() != "HOOKED" {
		t.Fatalf("hooked: %q %v", got.GetState(), err)
	}
	got, err = hooked.Loans.Update(ctx, &lendingv1.Loan{Name: loan.GetName(), Guarantor: "cards/2"}, []string{"guarantor"})
	if err != nil || got.GetState() != "HOOKED" {
		t.Fatalf("plain Update must leave state alone: %q %v", got.GetState(), err)
	}

	// The batch form.
	b := lending.NewBatch(w.conn)
	b.UpdateLoanWithOutputOnly(&lendingv1.Loan{Name: loan.GetName(), Etag: got.GetEtag(), Guarantor: "cards/1", State: "RETURNED"}, []string{"guarantor"}, []string{"state"})
	if err := b.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.lend.Loans.Get(ctx, loan.GetName()); got.GetState() != "RETURNED" {
		t.Fatalf("batch: %q", got.GetState())
	}
}

func TestPurgeKeepsReferencedRows(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, catalog.WithRetention(time.Millisecond))
	shelf, book := w.book(t)
	loan, err := w.lend.Loans.Create(ctx, &lendingv1.Loan{Book: book.GetName(), Borrower: "cards/1"})
	if err != nil {
		t.Fatal(err)
	}

	// The loan (another database) still names the book: the purge keeps it.
	if err := w.cat.Books.Delete(ctx, book.GetName()); err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Books.Purge(ctx, book.GetName()); err != nil {
		t.Fatalf("a purge never fails because of a referrer: %v", err)
	}
	if _, err := w.cat.Books.Get(ctx, book.GetName()); err != nil {
		t.Fatalf("book purged from under its loan: %v", err)
	}
	// The book is still on the shelf: the shelf is kept too.
	if err := w.cat.Shelfs.Purge(ctx, shelf.GetName()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cat.Shelfs.Get(ctx, shelf.GetName()); err != nil {
		t.Fatalf("shelf purged from under its book: %v", err)
	}
	want := []string{book.GetName() + ":foreign key", shelf.GetName() + ":foreign key"}
	if !slices.Equal(w.kept, want) {
		t.Fatalf("kept %v, want %v", w.kept, want)
	}

	// A batch purge keeps it too, without failing the batch.
	b := catalog.NewBatch(w.conn)
	b.PurgeBook(book.GetName())
	if err := b.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cat.Books.Get(ctx, book.GetName()); err != nil {
		t.Fatalf("batch purged a referenced book: %v", err)
	}

	// Once the loan is gone the book goes, then the shelf.
	if err := w.lend.Loans.Purge(ctx, loan.GetName()); err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Books.Purge(ctx, book.GetName()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cat.Books.Get(ctx, book.GetName()); !errors.Is(err, repox.ErrNotFound) {
		t.Fatalf("book not purged: %v", err)
	}
	if err := w.cat.Shelfs.Purge(ctx, shelf.GetName()); err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Shelfs.Purge(ctx, shelf.GetName()); !errors.Is(err, repox.ErrNotFound) {
		t.Fatalf("purging a gone shelf: %v", err)
	}
}

func TestPurgeExpired(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, catalog.WithRetention(time.Millisecond))
	shelf, kept := w.book(t)
	gone, err := w.cat.Books.Create(ctx, shelf.GetName(), &catalogv1.Book{Title: "Emma"})
	if err != nil {
		t.Fatal(err)
	}
	live, err := w.cat.Books.Create(ctx, shelf.GetName(), &catalogv1.Book{Title: "Ulysses"})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := w.cat.Books.Create(ctx, shelf.GetName(), &catalogv1.Book{Title: "Kim"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.lend.Loans.Create(ctx, &lendingv1.Loan{Book: kept.GetName(), Borrower: "cards/1"}); err != nil {
		t.Fatal(err)
	}
	// Another service says it refers to "remote" (store.v1.ReferenceService).
	w.refs.Referrers(bookType, repox.CheckerFunc(func(_ context.Context, _, name string) ([]repox.Referrer, error) {
		if name == remote.GetName() {
			return []repox.Referrer{{Name: "orders/1", ResourceType: "shop.example.com/Order", Field: "item", OnDelete: repox.OnDeleteAllow}}, nil
		}
		return nil, nil
	}))
	for _, b := range []*catalogv1.Book{kept, gone, remote} {
		if err := w.cat.Books.Delete(ctx, b.GetName()); err != nil {
			t.Fatal(err)
		}
	}
	res, err := w.cat.Books.PurgeExpired(ctx, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(res.Kept)
	wantKept := []string{kept.GetName(), remote.GetName()}
	slices.Sort(wantKept)
	if !slices.Equal(res.Purged, []string{gone.GetName()}) || !slices.Equal(res.Kept, wantKept) {
		t.Fatalf("purged %v kept %v", res.Purged, res.Kept)
	}
	if _, err := w.cat.Books.Get(ctx, live.GetName()); err != nil {
		t.Fatalf("a live book was purged: %v", err)
	}
	// Not yet expired: nothing to do.
	if res, err := w.cat.Books.PurgeExpired(ctx, time.Now().Add(-time.Hour)); err != nil || len(res.Purged)+len(res.Kept) != 0 {
		t.Fatalf("early sweep: %+v %v", res, err)
	}
}

func TestDeleteConsultsReferrers(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	_, book := w.book(t)
	rec := &recorder{}
	policy := repox.OnDeleteRestrict
	w.refs.Publisher("//catalog", rec).Referrers(bookType, repox.CheckerFunc(func(context.Context, string, string) ([]repox.Referrer, error) {
		return []repox.Referrer{{Name: "orders/1", ResourceType: "shop.example.com/Order", Field: "item", OnDelete: policy}}, nil
	}))

	err := w.cat.Books.Delete(ctx, book.GetName())
	if !errors.Is(err, repox.ErrReferenced) {
		t.Fatalf("restrict: %v", err)
	}
	if got, _ := w.cat.Books.Get(ctx, book.GetName()); got.GetDeleteTime() != nil {
		t.Fatal("a refused delete was written")
	}
	// The batch form refuses the whole commit.
	b := catalog.NewBatch(w.conn)
	b.DeleteBook(book.GetName())
	if err := b.Commit(ctx); !errors.Is(err, repox.ErrReferenced) {
		t.Fatalf("batch restrict: %v", err)
	}

	policy = repox.OnDeleteNotify
	if err := w.cat.Books.Delete(ctx, book.GetName()); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 || rec.events[0].Subject != book.GetName() || rec.events[0].Type != repox.EventTypeReferenceDeleted {
		t.Fatalf("events: %+v", rec.events)
	}
}

func TestReferrerSourcesAndReconcile(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	_, book := w.book(t)
	a, err := w.lend.Loans.Create(ctx, &lendingv1.Loan{Book: book.GetName(), Borrower: "cards/1", Guarantor: "cards/2"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.lend.Loans.Create(ctx, &lendingv1.Loan{Book: book.GetName(), Borrower: "cards/2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.lend.Loans.Delete(ctx, b.GetName()); err != nil {
		t.Fatal(err)
	}

	// What this service answers store.v1.ReferenceService with.
	got, err := repox.LocalReferrers(lending.ReferrerSources(w.lend)...).Referrers(ctx, cardType, "cards/2")
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, r := range got {
		seen = append(seen, r.Field+":"+r.Name+":"+r.OnDelete.String())
	}
	slices.Sort(seen)
	want := []string{
		"borrower:" + b.GetName() + ":ON_DELETE_RESTRICT", // deleted, still a referrer
		"guarantor:" + a.GetName() + ":ON_DELETE_NOTIFY",
	}
	if !slices.Equal(seen, want) {
		t.Fatalf("referrers %v, want %v", seen, want)
	}

	// The card service lost cards/2.
	delete(w.cards, "cards/2")
	var dangling []string
	rep, err := lending.ReconcileLoanReferences(ctx, w.lend.Loans, w.refs, repox.ReconcileOptions{
		OnDangling: func(_ context.Context, d repox.StoredReference) { dangling = append(dangling, d.Name+"."+d.Field) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Checked != 3 || rep.Dangling != 2 || len(dangling) != 2 {
		t.Fatalf("report %+v dangling %v", rep, dangling)
	}
}
