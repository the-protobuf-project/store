// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

// TestCanonicalVersion: every version a build can report is spelled with a
// leading "v", so a banner reads the same whether a release (goreleaser's bare
// "1.6.7") or a `go install` ("v1.6.7") wrote it.
func TestCanonicalVersion(t *testing.T) {
	for in, want := range map[string]string{
		"1.6.7":        "v1.6.7",
		"v1.6.7":       "v1.6.7",
		"1.6.7+dirty":  "v1.6.7+dirty",
		"v1.6.7+dirty": "v1.6.7+dirty",
		"dev":          "dev",
		"":             "",
	} {
		if got := canonicalVersion(in); got != want {
			t.Errorf("canonicalVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
