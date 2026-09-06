// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package gorm

// imports.go renders Go import statements for the generated files. orm does
// not run goimports over its output, so the backend emits already-grouped,
// already-sorted import blocks: a stdlib group and a third-party group separated
// by a blank line, matching the hand-written convention.

import "strings"

// importBlock renders an `import (...)` statement from ordered groups — each
// group's paths kept in the given order, groups separated by one blank line.
// Empty groups are dropped; when every group is empty it returns "" so a package
// with no imports emits none. The result has no leading or trailing newline; the
// template controls the surrounding blank lines.
func importBlock(groups ...[]string) string {
	var nonEmpty [][]string
	for _, g := range groups {
		if len(g) > 0 {
			nonEmpty = append(nonEmpty, g)
		}
	}
	if len(nonEmpty) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("import (\n")
	for i, g := range nonEmpty {
		if i > 0 {
			b.WriteByte('\n')
		}
		for _, p := range g {
			b.WriteString("\t\"")
			b.WriteString(p)
			b.WriteString("\"\n")
		}
	}
	b.WriteString(")")
	return b.String()
}
