// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package golang

import "strings"

// export uppercases the first letter of each underscore-separated part and
// joins them — WITHOUT Go initialism normalization ("id" → "Id", "uri" →
// "Uri", never "ID"/"URI"). GraphQL schema names are camelCase already, so
// this usually just capitalizes the first rune. It deliberately mirrors
// generateql's naming.Export: every identifier in the generated client (model
// fields, predicates, resource/domain fields, request getters) derives from a
// GraphQL name through this function, and consumers depend on that spelling —
// protokit's PascalGo would silently rename Id→ID and break drop-in
// compatibility with generateql-generated code.
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
