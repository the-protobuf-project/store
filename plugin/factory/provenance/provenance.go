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

// entityModule is the Go module carrying the entity.v1 annotations and the reader
// every protokit plugin shares. It is nested inside this repository but versions
// on its own tag, so its version is looked up like any other dependency's.
const entityModule = "github.com/the-protobuf-project/store/entity"

// protokitModule is the engine. It carries no annotations any more, but it still
// builds the IR, so it stays in the banner on its own line.
const protokitModule = "github.com/the-protobuf-project/protokit"

// unknown is the sentinel protoc-gen-go uses for a version it cannot determine,
// reused here so the banner reads consistently.
const unknown = "(unknown)"

// moduleVersion resolves a dependency's version from the build info the Go
// toolchain embeds. A test binary, a `go run` build, or a module replaced by a
// local directory may carry no version, in which case it stays unknown rather than
// guessing.
func moduleVersion(path string) string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return unknown
	}
	for _, dep := range bi.Deps {
		if dep.Path == path && dep.Version != "" {
			return dep.Version
		}
	}
	return unknown
}

// Both are resolved once per process: build info does not change under a running
// binary, and every generated file in a run carries the same banner.
var (
	entityVersion   = sync.OnceValue(func() string { return moduleVersion(entityModule) })
	protokitVersion = sync.OnceValue(func() string { return moduleVersion(protokitModule) })
)

// Render renders in's banner with the provenance lines appended, prefixed by
// prefix ("//" for Go, Prisma and TypeScript; "--" for SQL).
//
// runtimeModules names the modules the *generated* code imports — gorm.io/gorm
// for the stores, the opentelementry SDK for the telemetry adapter. Their
// versions are deliberately absent: the consumer's go.mod resolves those, not
// this plugin, and printing the version this binary happened to build against
// would be a plausible-looking lie. Omit the argument for output with no runtime
// dependency (DDL, Prisma schemas).
func Render(prefix string, in header.Info, runtimeModules ...string) string {
	in.Notes = append(in.Notes, notes(in.PluginVersion, runtimeModules)...)
	return header.Render(prefix, in)
}

// notes builds the provenance lines. store.v1 ships in this repository's root
// module, so it carries the plugin's own version by construction — there is no
// separate number to look up, and pretending otherwise would invite the two to
// drift. entity.v1 does not get that treatment: it is a separate module on a
// separate tag, and a consumer may well be running a plugin built against an older
// one than this repository's checkout contains.
func notes(pluginVersion string, runtimeModules []string) []string {
	if pluginVersion == "" {
		pluginVersion = unknown
	}
	out := []string{
		"annotations: entity.v1 " + entityVersion() + ", store.v1 " + pluginVersion,
		"engine:      protokit " + protokitVersion(),
	}
	if len(runtimeModules) > 0 {
		out = append(out, "runtime:     "+strings.Join(runtimeModules, ", "))
	}
	return out
}
