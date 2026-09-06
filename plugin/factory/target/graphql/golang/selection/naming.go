// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package selection

import "strings"

// export uppercases the first letter of each underscore-separated part and
// joins them — WITHOUT Go initialism normalization ("id" → "Id", never "ID").
// It mirrors generateql's naming.Export (see the parent package's naming.go):
// model struct fields must keep the generateql spelling so generated clients
// stay drop-in compatible.
func export(name string) string {
	parts := strings.Split(name, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		r := []rune(p)
		r[0] = []rune(strings.ToUpper(string(r[0])))[0]
		b.WriteString(string(r))
	}
	return b.String()
}
