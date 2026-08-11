// Package provenance renders the generated-file banner with the full set of
// modules that decided the output.
//
// The banner has always named the plugin and protoc. That was enough when a
// plugin's annotations were its own, but the vocabulary is now split across
// modules that version independently: protokit.v1 ships in protokit, store.v1
// ships here, and the generated code's runtime is a third module again. Output
// can therefore change without this plugin changing — a protokit bump moves the
// neutral structure — and a banner naming only the plugin points at the wrong
// thing when someone asks why a file differs.
//
// So every banner records the triple: plugin@version, annotation modules@version,
// runtime module. The plugin's own version arrives on header.Info; protokit's is
// read from the binary's build info, which is the only place that knows what it
// was actually linked against.
package provenance

import (
	"runtime/debug"
	"strings"
	"sync"

	"github.com/the-protobuf-project/protokit/header"
)

// protokitModule is the Go module carrying the protokit.v1 annotations.
const protokitModule = "github.com/the-protobuf-project/protokit"

// unknown is the sentinel protoc-gen-go uses for a version it cannot determine,
// reused here so the banner reads consistently.
const unknown = "(unknown)"

// protokitVersion is resolved once per process from the build info the Go
// toolchain embeds. A test binary or `go run` build may carry no module graph, in
// which case it stays unknown rather than guessing.
var protokitVersion = sync.OnceValue(func() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return unknown
	}
	for _, dep := range bi.Deps {
		if dep.Path == protokitModule && dep.Version != "" {
			return dep.Version
		}
	}
	return unknown
})

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

// notes builds the provenance lines. store.v1 ships in this repository, so it
// carries the plugin's own version by construction — there is no separate number
// to look up, and pretending otherwise would invite the two to drift.
func notes(pluginVersion string, runtimeModules []string) []string {
	if pluginVersion == "" {
		pluginVersion = unknown
	}
	out := []string{
		"annotations: protokit.v1 " + protokitVersion() + ", store.v1 " + pluginVersion,
	}
	if len(runtimeModules) > 0 {
		out = append(out, "runtime:     "+strings.Join(runtimeModules, ", "))
	}
	return out
}
