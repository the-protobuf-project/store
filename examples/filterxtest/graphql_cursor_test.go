// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package filterxtest

import (
	"testing"

	"github.com/the-protobuf-project/store/examples/generated/gorm/bookstore_db/bookstorev1"
	"github.com/the-protobuf-project/store/examples/generated/gorm/filterx"
)

// gqlBook is a row as the graphql client target generates it: camelCase field
// names in graphql tags, no json tags.
type gqlBook struct {
	Id         string `graphql:"id"`
	Title      string `graphql:"title"`
	CreateTime string `graphql:"createTime"`
}

// jsonBook is a row decoded from a JSON response.
type jsonBook struct {
	Id         string `json:"id"`
	CreateTime string `json:"createTime,omitempty"`
}

// aliasedBook selects its columns under aliases and with arguments, which the
// graphql tag spells out but the response field does not change.
type aliasedBook struct {
	Key     string `graphql:"key: id"`
	Created string `graphql:"created: createTime(format: RFC3339)"`
}

// A full page on the Hasura engine mints a next-page token from the last row.
// The rows are the graphql client's models, which carry graphql tags only:
// reading json tags found no column and failed every second page.
func TestNextPageReadsGraphQLTags(t *testing.T) {
	terms, err := filterx.OrderTerms(bookstorev1.BookFilterSpec, "create_time")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		rows func() (string, error)
	}{
		{"graphql tags", func() (string, error) {
			_, tok, err := filterx.NextPage([]gqlBook{
				{Id: "01A", CreateTime: "2026-01-01T00:00:00Z"},
				{Id: "01B", CreateTime: "2026-01-02T00:00:00Z"},
				{Id: "01C", CreateTime: "2026-01-03T00:00:00Z"},
			}, 2, terms, filterx.CamelCase, filterx.GraphQLColumn)
			return tok, err
		}},
		{"json tags", func() (string, error) {
			_, tok, err := filterx.NextPage([]jsonBook{
				{Id: "01A", CreateTime: "2026-01-01T00:00:00Z"},
				{Id: "01B", CreateTime: "2026-01-02T00:00:00Z"},
				{Id: "01C", CreateTime: "2026-01-03T00:00:00Z"},
			}, 2, terms, filterx.CamelCase, filterx.GraphQLColumn)
			return tok, err
		}},
		{"aliases and arguments", func() (string, error) {
			_, tok, err := filterx.NextPage([]aliasedBook{
				{Key: "01A", Created: "2026-01-01T00:00:00Z"},
				{Key: "01B", Created: "2026-01-02T00:00:00Z"},
				{Key: "01C", Created: "2026-01-03T00:00:00Z"},
			}, 2, terms, filterx.CamelCase, filterx.GraphQLColumn)
			return tok, err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok, err := tc.rows()
			if err != nil {
				t.Fatalf("NextPage: %v", err)
			}
			if tok == "" {
				t.Fatal("a full page minted no next-page token")
			}
			vals, err := filterx.DecodeCursor(terms, tok)
			if err != nil {
				t.Fatal(err)
			}
			// The cursor is the last row kept: the second.
			if len(vals) != 2 || *vals[0] != "2026-01-02T00:00:00Z" || *vals[1] != "01B" {
				t.Fatalf("cursor values %v", values(vals))
			}
		})
	}
}

func values(vals []*string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = deref(v)
	}
	return out
}
