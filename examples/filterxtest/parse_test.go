// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package filterxtest

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/the-protobuf-project/store/examples/generated/gorm/bookstore_db/bookstorev1"
	"github.com/the-protobuf-project/store/examples/generated/gorm/filterx"
)

// show renders conditions compactly: leaves as field<op>value, composites as
// OR(..), AND(..), NOT(..), free text as ~value.
func show(conds []filterx.Condition) string {
	parts := make([]string, 0, len(conds))
	for _, c := range conds {
		parts = append(parts, showOne(c))
	}
	return strings.Join(parts, " ")
}

func showOne(c filterx.Condition) string {
	switch c.Op {
	case filterx.OpOr:
		return "OR(" + show(c.Any) + ")"
	case filterx.OpAnd:
		return "AND(" + show(c.Any) + ")"
	case filterx.OpNot:
		return "NOT(" + show(c.Any) + ")"
	}
	if c.Field == "" {
		return "~" + c.Value
	}
	op := map[filterx.Op]string{filterx.OpEq: "=", filterx.OpNeq: "!=", filterx.OpHas: ":", filterx.OpLt: "<", filterx.OpLte: "<=", filterx.OpGt: ">", filterx.OpGte: ">="}[c.Op]
	return c.Field + op + c.Value
}

func TestParseBooleanGrammar(t *testing.T) {
	for filter, want := range map[string]string{
		``:                                   ``,
		`a = 1`:                              `a=1`,
		`a = 1 b = 2`:                        `a=1 b=2`,
		`a = 1 AND b = 2`:                    `a=1 b=2`,
		`a = 1 and b = 2`:                    `a=1 b=2`,
		`a = 1 OR b = 2`:                     `OR(a=1 b=2)`,
		`a = 1 OR b = 2 OR c = 3`:            `OR(a=1 b=2 c=3)`,
		`a = 1 b = 2 OR c = 3`:               `a=1 OR(b=2 c=3)`, // OR binds tighter than AND
		`a = 1 AND b = 2 OR c = 3`:           `a=1 OR(b=2 c=3)`,
		`(a = 1 AND b = 2) OR c = 3`:         `OR(AND(a=1 b=2) c=3)`,
		`(a = 1 b = 2)`:                      `a=1 b=2`, // a top-level group flattens
		`((a = 1))`:                          `a=1`,
		`NOT a = 1`:                          `NOT(a=1)`,
		`-a = 1`:                             `NOT(a=1)`,
		`-dune`:                              `NOT(~dune)`,
		`-"two words"`:                       `NOT(~two words)`,
		`NOT (a = 1 OR b = 2)`:               `NOT(OR(a=1 b=2))`,
		`-(a = 1 b = 2) c > -5`:              `NOT(AND(a=1 b=2)) c>-5`,
		`NOT a = 1 OR b = 2`:                 `OR(NOT(a=1) b=2)`,
		`a:"x OR y"`:                         `a:x OR y`, // quoted keywords are values
		`"OR"`:                               `~OR`,
		`or`:                                 `~or`, // OR and NOT are capitals only
		`a = 1 AND (b = 2 OR (c = 3 d = 4))`: `a=1 OR(b=2 AND(c=3 d=4))`,
	} {
		conds, err := filterx.Parse(filter)
		if err != nil {
			t.Errorf("Parse(%q): %v", filter, err)
			continue
		}
		if got := show(conds); got != want {
			t.Errorf("Parse(%q) = %s, want %s", filter, got, want)
		}
	}
}

func TestParseRejectsMalformedBooleans(t *testing.T) {
	for _, filter := range []string{
		`a = 1 OR`,
		`OR a = 1 OR`,
		`a = 1 OR AND b = 2`,
		`NOT`,
		`a = 1 NOT`,
		`(a = 1`,
		`a = 1)`,
		`()`,
		`- a = 1`,
		`- "a"`,
		`a = (`,
		strings.Repeat("(", 40) + "a = 1" + strings.Repeat(")", 40),
		strings.Repeat("NOT ", 40) + "a = 1",
	} {
		if conds, err := filterx.Parse(filter); !errors.Is(err, filterx.ErrInvalid) {
			t.Errorf("Parse(%q) = %s, %v; want ErrInvalid", filter, show(conds), err)
		}
	}
}

func TestBooleansRenderOnEveryEngine(t *testing.T) {
	conds, err := filterx.Parse(`(genre = FANTASY OR published_year < 1950) -title:"dune" NOT isbn = "x"`)
	if err != nil {
		t.Fatal(err)
	}
	where, args, err := engine().Where(conds)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{" OR ", "NOT coalesce((", "ILIKE", ` AND `} {
		if !strings.Contains(where, want) {
			t.Errorf("SQL %q lacks %q", where, want)
		}
	}
	if len(args) != 4 {
		t.Errorf("SQL args = %v, want 4", args)
	}

	q, err := filterx.OpenSearch(bookstorev1.BookFilterSpec).Query(conds)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(q)
	for _, want := range []string{`"should"`, `"must_not"`, `"wildcard"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("OpenSearch query %s lacks %s", raw, want)
		}
	}
	if n := strings.Count(string(raw), `"must_not"`); n != 2 {
		t.Errorf("OpenSearch query has %d must_not, want 2: %s", n, raw)
	}

	p, ok, err := filterx.Hasura[bookstorev1.Book](bookstorev1.BookFilterSpec, nil).Predicate(conds)
	if err != nil || !ok {
		t.Fatalf("Hasura Predicate = %v, %v", ok, err)
	}
	raw, _ = json.Marshal(p)
	for _, want := range []string{`"_or"`, `"_not"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("Hasura predicate %s lacks %s", raw, want)
		}
	}
}

// An override serves its field inside OR and NOT too, not only at the top.
func TestOverridesApplyInsideBooleans(t *testing.T) {
	conds, err := filterx.Parse(`title = "a" OR NOT state = open`)
	if err != nil {
		t.Fatal(err)
	}
	where, _, err := engine().Override("state", func(c filterx.Condition) (string, []any, error) {
		return "derived_state = ?", []any{c.Value}, nil
	}).Where(conds)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(where, "NOT coalesce((derived_state = ?), false)") {
		t.Errorf("override inside NOT: %q", where)
	}
}
