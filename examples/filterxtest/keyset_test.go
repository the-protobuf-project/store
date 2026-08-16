// Package filterxtest exercises the generated filterx paging engine against the
// bookstore example's specs. It lives outside the generated tree so it only
// reaches the engine's exported surface — the same surface a caller uses.
package filterxtest

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/the-protobuf-project/store/examples/generated/gorm/bookstore_db/bookstorev1"
	"github.com/the-protobuf-project/store/examples/generated/gorm/filterx"
)

// engine builds a gorm engine over the Book spec, whose sort surface covers a
// text, an enum, an int and a timestamp column plus the ULID primary key.
func engine() *filterx.GormEngine[bookstorev1.Book] {
	return filterx.Gorm[bookstorev1.Book](bookstorev1.BookFilterSpec)
}

// firstPage is the empty page token: paging starts without a seek clause.
func TestSeekFirstPageHasNoClause(t *testing.T) {
	clause, args, err := engine().Seek("title", "")
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if clause != "" || len(args) != 0 {
		t.Fatalf("first page should not seek, got %q with %d args", clause, len(args))
	}
}

// The PK tiebreaker means even a single-column order_by pages on two terms, so
// the predicate is the two-branch lexicographic form rather than a bare `>`.
func TestSeekIsLexicographic(t *testing.T) {
	e := engine()
	token := mint(t, e, "title", []*string{ptr("Dune"), ptr("01J0")})

	clause, args, err := e.Seek("title", token)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}
	// Branch 1: sorts after on title. Branch 2: ties on title, sorts after on id.
	if got, want := strings.Count(clause, " OR "), 1; got != want {
		// title is NOT NULL and so is the id tiebreaker, so the only OR is the one
		// joining the two branches.
		t.Errorf("expected %d OR in %q, got %d", want, clause, got)
	}
	if !strings.Contains(clause, `"title" > ?`) {
		t.Errorf("missing strict comparison on the leading term: %q", clause)
	}
	if !strings.Contains(clause, `"title" = ?`) {
		t.Errorf("missing tie on the leading term: %q", clause)
	}
	if !strings.Contains(clause, `"id" > ?`) {
		t.Errorf("missing tiebreak on the primary key: %q", clause)
	}
	if len(args) != 3 { // title >, then (title =, id >)
		t.Errorf("expected 3 args, got %d: %v", len(args), args)
	}
}

// A DESC term must compare with < , not >; getting this backwards silently pages
// in the wrong direction rather than failing.
func TestSeekHonorsDescendingDirection(t *testing.T) {
	e := engine()
	token := mint(t, e, "published_year desc", []*string{ptr("1965"), ptr("01J0")})

	clause, _, err := e.Seek("published_year desc", token)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if !strings.Contains(clause, `"published_year" < ?`) {
		t.Errorf("DESC term should compare with <, got %q", clause)
	}
	if strings.Contains(clause, `"published_year" > ?`) {
		t.Errorf("DESC term must not compare with >, got %q", clause)
	}
}

// Cursor values are typed by their column's kind, so an int column seeks with an
// int64 rather than the string the token carries.
func TestSeekArgsAreTypedByKind(t *testing.T) {
	e := engine()
	token := mint(t, e, "published_year", []*string{ptr("1965"), ptr("01J0")})

	_, args, err := e.Seek("published_year", token)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if len(args) == 0 {
		t.Fatal("expected seek args")
	}
	if _, ok := args[0].(int64); !ok {
		t.Errorf("published_year arg should be int64, got %T (%v)", args[0], args[0])
	}
}

// ASC sorts NULLs last, so rows after a non-null cursor include the trailing
// NULL block. Omitting that branch silently drops those rows from the last page.
func TestSeekAscIncludesTrailingNulls(t *testing.T) {
	e := engine()
	token := mint(t, e, "isbn", []*string{ptr("978"), ptr("01J0")})

	clause, _, err := e.Seek("isbn", token)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if !strings.Contains(clause, `"isbn" IS NULL`) {
		t.Errorf("ASC seek should admit trailing NULLs, got %q", clause)
	}
}

// A NULL cursor value on an ASC term has nothing after it on that term, so that
// branch collapses and only the tie-then-tiebreak branch survives.
func TestSeekAscNullCursorCollapsesToTiebreak(t *testing.T) {
	e := engine()
	token := mint(t, e, "isbn", []*string{nil, ptr("01J0")})

	clause, args, err := e.Seek("isbn", token)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if strings.Contains(clause, `"isbn" > ?`) {
		t.Errorf("nothing sorts after an ASC NULL, got %q", clause)
	}
	if !strings.Contains(clause, `"isbn" IS NULL`) {
		t.Errorf("tie branch should match the NULL, got %q", clause)
	}
	if !strings.Contains(clause, `"id" > ?`) {
		t.Errorf("expected the PK tiebreak, got %q", clause)
	}
	if len(args) != 1 { // only the id bound; the NULL tie takes no argument
		t.Errorf("expected 1 arg, got %d: %v", len(args), args)
	}
}

// A NOT NULL column has no NULL half to admit, and the bare `> ?` it reduces to
// is what lets PostgreSQL serve the seek as an index range scan. The primary-key
// tiebreaker rides on every query, so this applies to all of them.
func TestSeekOmitsNullBranchForNotNullColumns(t *testing.T) {
	e := engine()
	token := mint(t, e, "title", []*string{ptr("Dune"), ptr("01J0")})

	clause, _, err := e.Seek("title", token)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if strings.Contains(clause, `"id" IS NULL`) {
		t.Errorf("primary key cannot be NULL, so the branch is dead weight: %q", clause)
	}
	if strings.Contains(clause, `"title" IS NULL`) {
		t.Errorf("title is NOT NULL, so the branch is dead weight: %q", clause)
	}
}

// Tokens minted by the previous offset scheme were base64 of a bare integer.
// They must fail loudly rather than silently restarting at page 1.
func TestStaleOffsetTokenIsRejected(t *testing.T) {
	// base64url("100"), exactly what EncodeOffset used to produce.
	if _, _, err := engine().Seek("title", "MTAw"); !errors.Is(err, filterx.ErrInvalid) {
		t.Fatalf("stale offset token should be ErrInvalid, got %v", err)
	}
}

// Replaying a token under a different order_by would seek against the wrong
// columns, so the token records its columns and is rejected on mismatch.
func TestTokenFromAnotherOrderByIsRejected(t *testing.T) {
	e := engine()
	token := mint(t, e, "title", []*string{ptr("Dune"), ptr("01J0")})

	if _, _, err := e.Seek("published_year", token); !errors.Is(err, filterx.ErrInvalid) {
		t.Fatalf("cross-order_by token should be ErrInvalid, got %v", err)
	}
}

func TestMalformedTokenIsRejected(t *testing.T) {
	if _, _, err := engine().Seek("title", "!!!not base64!!!"); !errors.Is(err, filterx.ErrInvalid) {
		t.Fatalf("malformed token should be ErrInvalid, got %v", err)
	}
}

// NextPage mints the cursor by reflecting the last kept row's sort columns out
// of the model, and only when a further page exists.
func TestNextPageMintsCursorFromLastKeptRow(t *testing.T) {
	terms, err := filterx.OrderTerms(bookstorev1.BookFilterSpec, "title")
	if err != nil {
		t.Fatalf("OrderTerms: %v", err)
	}
	rows := []bookstorev1.Book{
		{ID: "01J0", Title: "A"},
		{ID: "01J1", Title: "B"},
		{ID: "01J2", Title: "C"}, // the limit+1 probe row
	}

	page, token, err := filterx.NextPage(rows, 2, terms, filterx.SameColumn, filterx.GormColumn)
	if err != nil {
		t.Fatalf("NextPage: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("page should be trimmed to the limit, got %d rows", len(page))
	}
	if token == "" {
		t.Fatal("a further page exists, so a token was expected")
	}
	// The cursor addresses the last row *kept*, not the probe row.
	vals, err := filterx.DecodeCursor(terms, token)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if vals[0] == nil || *vals[0] != "B" {
		t.Errorf("cursor should address the last kept row (B), got %v", deref(vals[0]))
	}
}

func TestNextPageOnLastPageMintsNoToken(t *testing.T) {
	terms, err := filterx.OrderTerms(bookstorev1.BookFilterSpec, "title")
	if err != nil {
		t.Fatalf("OrderTerms: %v", err)
	}
	rows := []bookstorev1.Book{{ID: "01J0", Title: "A"}}

	page, token, err := filterx.NextPage(rows, 2, terms, filterx.SameColumn, filterx.GormColumn)
	if err != nil {
		t.Fatalf("NextPage: %v", err)
	}
	if len(page) != 1 || token != "" {
		t.Fatalf("short page should end pagination, got %d rows and token %q", len(page), token)
	}
}

// Every kind a cursor can carry must survive the token round trip as the typed
// value its column compares against.
func TestCursorValueRoundTripsByKind(t *testing.T) {
	ts := time.Date(2026, 8, 15, 10, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		kind filterx.Kind
		text string
		want any
	}{
		{"text", filterx.KindText, "Dune", "Dune"},
		{"enum", filterx.KindEnum, "GENRE_SCIFI", "GENRE_SCIFI"},
		{"int", filterx.KindInt, "1965", int64(1965)},
		{"float", filterx.KindFloat, "4.5", 4.5},
		{"bool", filterx.KindBool, "true", true},
		{"timestamp", filterx.KindTimestamp, ts.Format(time.RFC3339Nano), ts},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := filterx.ParseCursorValue(&tc.text, tc.kind)
			if err != nil {
				t.Fatalf("ParseCursorValue: %v", err)
			}
			if gotT, ok := got.(time.Time); ok {
				if !gotT.Equal(tc.want.(time.Time)) {
					t.Errorf("got %v, want %v", gotT, tc.want)
				}
				return
			}
			if got != tc.want {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestNilCursorValueStaysNull(t *testing.T) {
	got, err := filterx.ParseCursorValue(nil, filterx.KindText)
	if err != nil {
		t.Fatalf("ParseCursorValue: %v", err)
	}
	if got != nil {
		t.Errorf("a NULL cursor value should stay nil, got %#v", got)
	}
}

func TestPageLimitClamps(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   int32
		want int
	}{
		{"unset defaults", 0, 50},
		{"negative defaults", -1, 50},
		{"honored", 10, 10},
		{"clamped to max", 5000, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := filterx.PageLimit(filterx.ListInput{PageSize: tc.in}); got != tc.want {
				t.Errorf("PageLimit(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// mint builds a token for vals under orderBy, the way a previous page would
// have. It fails the test rather than returning an error so callers stay terse.
func mint(t *testing.T, _ *filterx.GormEngine[bookstorev1.Book], orderBy string, vals []*string) string {
	t.Helper()
	terms, err := filterx.OrderTerms(bookstorev1.BookFilterSpec, orderBy)
	if err != nil {
		t.Fatalf("OrderTerms(%q): %v", orderBy, err)
	}
	if len(terms) != len(vals) {
		t.Fatalf("order_by %q resolved to %d terms, got %d cursor values", orderBy, len(terms), len(vals))
	}
	token, err := filterx.EncodeCursor(terms, vals)
	if err != nil {
		t.Fatalf("EncodeCursor: %v", err)
	}
	return token
}

func ptr(s string) *string { return &s }

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
