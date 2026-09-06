// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package provenance renders the generated-file banner with the full set of
// modules that decided the output.
//
// The banner has always named the plugin and protoc. That was enough when a
// plugin's annotations were its own, but the vocabulary is now split across
// modules that version independently: entity.v1 ships in this repository's nested
// entity/ module, store.v1 ships in this one, and the generated code's runtime is
// a third module again. Output can therefore change without this plugin changing,
// and a banner naming only the plugin points at the wrong thing when someone asks
// why a file differs.
//
// The engine gets its own line rather than sharing the annotations one. protokit
// used to appear there because it owned the neutral vocabulary (protokit.v1); it
// no longer owns any vocabulary, but it still decides the structure — a protokit
// bump can move a derived name without a single annotation changing. Dropping it
// from the banner along with its vocabulary would have quietly removed the answer
// to the most common version of "why did this file change?".
//
// So every banner records: plugin@version, annotation modules@version, the engine,
// and the runtime modules. The plugin's own version arrives on header.Info; the
// others are read from the binary's build info, which is the only place that knows
// what it was actually linked against.
package provenance

import (
	"runtime/debug"
	"strings"
	"sync"

	"github.com/the-protobuf-project/protokit/header"
)

// protokitModule is the engine. It carries no annotations any more, but it still
// builds the IR, so it stays in the banner on its own line.
const protokitModule = "github.com/the-protobuf-project/protokit"

// Unknown is the sentinel protoc-gen-go uses for a version it cannot determine,
// reused here so the banner reads consistently. It is exported because the
// golden tests pin the engine version to it; see SetEngineVersion.
const Unknown = "(unknown)"

// moduleVersion resolves a dependency's version from the build info the Go
// toolchain embeds. A test binary, a `go run` build, or a module replaced by a
// local directory may carry no version, in which case it stays unknown rather than
// guessing.
func moduleVersion(path string) string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return Unknown
	}
	for _, dep := range bi.Deps {
		if dep.Path == path && dep.Version != "" {
			return dep.Version
		}
	}
	return Unknown
}

// Resolved once per process: build info does not change under a running binary,
// and every generated file in a run carries the same banner. It is a variable so
// SetEngineVersion can replace the lookup.
var protokitVersion = sync.OnceValue(func() string { return moduleVersion(protokitModule) })

// SetEngineVersion pins the engine version stamped into every banner, replacing
// the build-info lookup.
//
// The golden tests call this, because that lookup is not reproducible. What it
// finds depends on how the build resolved protokit — a module dependency carries
// its version, a local `replace` carries none — and, for a test binary, on the
// toolchain: Go 1.27 records dependency versions that Go 1.26 left empty. A
// byte-for-byte golden generated under one of those answers fails under another,
// which is a property of the harness rather than of the output, so the tests take
// the variable out of the comparison instead of encoding one machine's answer.
func SetEngineVersion(v string) { protokitVersion = func() string { return v } }

// license is the copyright/licence block placed above every generated banner,
// set once at startup from the license_header opt. It is empty by default, and
// deliberately so: generated code belongs to whoever ran the generator, not to
// this plugin, so stamping a copyright line is something an invocation opts into
// rather than something every downstream tree inherits.
var license []string

// SetLicense sets the licence block from the license_header opt. Blank input
// clears it.
//
// The lines carry no comment markers. Render applies the target's own prefix, so
// one header file serves Go, Prisma and TypeScript ("//") and SQL ("--") alike —
// which a file of pre-commented lines could not do.
func SetLicense(text string) {
	license = nil
	text = strings.Trim(text, "\n")
	if text == "" {
		return
	}
	for _, ln := range strings.Split(text, "\n") {
		license = append(license, strings.TrimRight(ln, " \t"))
	}
}

// Render renders in's banner with the provenance lines appended, prefixed by
// prefix ("//" for Go, Prisma and TypeScript; "--" for SQL). When a licence
// block is set it is emitted above the banner, separated by a genuinely empty
// line rather than a bare-prefix one: that keeps the two comment blocks
// detached, so Go reads the licence as a file comment instead of folding it into
// the package doc, and leaves "Code generated ... DO NOT EDIT." still standing
// ahead of the first non-comment line where the toolchain looks for it.
//
// runtimeModules names the modules the *generated* code imports — gorm.io/gorm
// for the stores, the telemetry SDK for the telemetry adapter. Their
// versions are deliberately absent: the consumer's go.mod resolves those, not
// this plugin, and printing the version this binary happened to build against
// would be a plausible-looking lie. Omit the argument for output with no runtime
// dependency (DDL, Prisma schemas).
func Render(prefix string, in header.Info, runtimeModules ...string) string {
	in.Notes = append(in.Notes, notes(in.PluginVersion, runtimeModules)...)
	banner := header.Render(prefix, in)
	if len(license) == 0 {
		return banner
	}
	var b strings.Builder
	for _, ln := range license {
		if ln == "" {
			b.WriteString(prefix)
		} else {
			b.WriteString(prefix + " " + ln)
		}
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(banner)
	return b.String()
}

// notes builds the provenance lines. Both vocabularies ship in this repository's
// root module, so each carries the plugin's own version by construction — there is
// no separate number to look up, and pretending otherwise would invite them to
// drift. Reading entity.v1's version out of build info would report "(unknown)"
// forever now that it is part of the main module rather than a dependency.
func notes(pluginVersion string, runtimeModules []string) []string {
	if pluginVersion == "" {
		pluginVersion = Unknown
	}
	out := []string{
		"annotations: entity.v1 " + pluginVersion + ", store.v1 " + pluginVersion,
		"engine:      protokit " + protokitVersion(),
	}
	if len(runtimeModules) > 0 {
		out = append(out, "runtime:     "+strings.Join(runtimeModules, ", "))
	}
	return out
}
