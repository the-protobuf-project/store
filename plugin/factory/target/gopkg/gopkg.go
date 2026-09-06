// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package gopkg resolves the Go package name protoc-gen-go gave each generated
// import path, so this plugin's output qualifies proto types exactly as the
// .pb.go files spell them.
//
// The name is not recoverable from the import path. protoc-gen-go takes it from
// the ";name" suffix on the go_package option (or on an M<file>=<path>;<name>
// flag) when one is present, and only falls back to the path's final segment
// otherwise. A package generated from `option go_package = ".../resource/v1;resourcev1"`
// therefore declares `package resourcev1` while its path ends in `v1`, and a
// generator that derives the qualifier from the path emits `v1.Event` against an
// unaliased import — code that does not compile.
//
// Deriving it independently is also unnecessary: protogen hands every plugin the
// name it resolved, by the same rules, from the same CodeGeneratorRequest that
// produced the .pb.go files. Index records that answer, and every target reads
// package names back through it rather than re-deriving one. One derivation, in
// one place, agreeing with protoc-gen-go by construction rather than by each
// target guessing alike.
package gopkg

import (
	"strings"

	"google.golang.org/protobuf/compiler/protogen"
)

// Names maps a generated Go import path to the package name declared at it.
type Names map[string]string

// Index records the package name protogen resolved for every file in the
// codegen request — the files to generate and everything they import, which is
// every file a GoIdent reaching a target can come from.
func Index(p *protogen.Plugin) Names {
	n := make(Names, len(p.Files))
	for _, f := range p.Files {
		n[string(f.GoImportPath)] = string(f.GoPackageName)
	}
	return n
}

// Of returns the package name declared at path. Paths outside the codegen
// request — the hand-written runtime deps a converter reaches for, and this
// plugin's own generated packages — fall back to the final segment, which is
// both protogen's default and true by construction for the packages we emit.
func (n Names) Of(path string) string {
	if name := n[path]; name != "" {
		return name
	}
	return LastSegment(path)
}

// Alias returns the import alias needed to reference path as Of(path): empty
// when the declared name already matches the final segment, so the usual import
// stays bare, and the package name otherwise. Aliasing only on a mismatch keeps
// the alias out of the common case, where gofmt would read it as redundant.
func (n Names) Alias(path string) string {
	name := n.Of(path)
	if name == LastSegment(path) {
		return ""
	}
	return name
}

// LastSegment is the final element of a slash-separated import path — the name
// a bare import binds when the package declares no different one.
func LastSegment(path string) string {
	return path[strings.LastIndex(path, "/")+1:]
}
