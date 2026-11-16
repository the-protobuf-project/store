// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package references_test

// ifmatch_test.go drives repox.IfMatch against Postgres: Delete and Undelete
// apply the etag in the statement that writes, so a write landing between a
// caller's read and its delete is caught, and without one nothing changes.

import (
	"context"
	"errors"
	"testing"

	"github.com/the-protobuf-project/store/examples/generated/go/protobuf/references/catalogv1"
	"github.com/the-protobuf-project/store/examples/generated/go/repository/references/repox"
)

func TestIfMatchDelete(t *testing.T) {
	w := newWorld(t)
	_, b := w.book(t)
	ctx := context.Background()

	// A write lands between the caller's read (b) and its delete: the delete,
	// guarded by the etag it read, is refused and the record left live.
	updated, err := w.cat.Books.Update(ctx, &catalogv1.Book{Name: b.GetName(), Title: "Dune Messiah"}, []string{"title"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.cat.Books.Delete(repox.IfMatch(ctx, b.GetEtag()), b.GetName()); !errors.Is(err, repox.ErrConflict) {
		t.Fatalf("Delete with a stale etag = %v, want ErrConflict", err)
	}
	if got, err := w.cat.Books.Get(ctx, b.GetName()); err != nil || got.GetDeleteTime() != nil {
		t.Fatalf("a refused delete changed the record: %v, %v", got, err)
	}

	// The current etag passes.
	if err := w.cat.Books.Delete(repox.IfMatch(ctx, updated.GetEtag()), b.GetName()); err != nil {
		t.Fatalf("Delete with the current etag = %v", err)
	}
	deleted, err := w.cat.Books.Get(ctx, b.GetName())
	if err != nil || deleted.GetDeleteTime() == nil {
		t.Fatalf("not deleted: %v, %v", deleted, err)
	}
	// Deleted already: not found, whatever the etag.
	if err := w.cat.Books.Delete(repox.IfMatch(ctx, deleted.GetEtag()), b.GetName()); !errors.Is(err, repox.ErrNotFound) {
		t.Fatalf("Delete of a deleted record = %v, want ErrNotFound", err)
	}

	// Undelete is guarded the same way: a stale etag leaves it deleted.
	if _, err := w.cat.Books.Undelete(repox.IfMatch(ctx, updated.GetEtag()), b.GetName()); !errors.Is(err, repox.ErrConflict) {
		t.Fatalf("Undelete with a stale etag = %v, want ErrConflict", err)
	}
	if got, _ := w.cat.Books.Get(ctx, b.GetName()); got.GetDeleteTime() == nil {
		t.Fatal("a refused undelete restored the record")
	}
	restored, err := w.cat.Books.Undelete(repox.IfMatch(ctx, deleted.GetEtag()), b.GetName())
	if err != nil || restored.GetDeleteTime() != nil {
		t.Fatalf("Undelete with the current etag = %v, %v", restored, err)
	}
	// Live already: not deleted, whatever the etag.
	if _, err := w.cat.Books.Undelete(repox.IfMatch(ctx, "stale"), b.GetName()); !errors.Is(err, repox.ErrAlreadyExists) {
		t.Fatalf("Undelete of a live record = %v, want ErrAlreadyExists", err)
	}

	// Without a precondition nothing changes: the delete is unguarded.
	if err := w.cat.Books.Delete(ctx, b.GetName()); err != nil {
		t.Fatalf("unguarded Delete = %v", err)
	}
	if repox.IfMatchOf(repox.IfMatch(ctx, "")) != "" {
		t.Fatal("an empty etag carried a precondition")
	}
}
