// Package graphql is the "graphql" factory Target: it renders a typed GraphQL
// client from the Model's GraphQL facet. The parsing that produced that facet is
// language-agnostic (in source/graphql); this Target only picks the per-language
// emitter and carries its output configuration. The Go emitter lives under golang/;
// adding another language is a new sub-emitter selected in Generate, not a rewrite.
package graphql

import (
	"fmt"
	"path"
	"strings"

	"github.com/the-protobuf-project/protokit/factory"
	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"github.com/the-protobuf-project/store/plugin/factory/coreir"
	"github.com/the-protobuf-project/store/plugin/factory/target/graphql/golang"
)

// defaultRuntimeModule is the import path of the Go runtime facade generated
// clients depend on; the sibling predicate-DSL package is derived from it.
const defaultRuntimeModule = "github.com/the-protobuf-project/runtime-go/network/runtime"

// Config is the output configuration for one client generation.
type Config struct {
	OutDir        string            // parent dir; client lands in <OutDir>/<Package>/
	Package       string            // root package name; default: base(GoModule)+"ql"
	GoModule      string            // import path of the generated root package (required)
	RuntimeModule string            // runtime facade import path; default defaultRuntimeModule
	MaxDepth      int               // relation inlining depth (default 1)
	Scalars       map[string]string // GraphQL scalar -> Go type overrides
	Dialect       dialect.Dialect   // engine conventions; default dialect.Default()
	Version       string            // plugin version stamped into generated banners

	// Sink, when set, receives each generated file (path relative to the package
	// root, content) instead of writing to OutDir. The plugin sets it to route
	// output through the protoc response.
	Sink func(relPath string, content []byte) error
}

// Target renders the GraphQL client.
type Target struct{ cfg Config }

// New returns a graphql-client target, applying defaults.
func New(cfg Config) *Target {
	if cfg.Dialect == nil {
		cfg.Dialect = dialect.Default()
	}
	if cfg.RuntimeModule == "" {
		cfg.RuntimeModule = defaultRuntimeModule
	}
	if cfg.MaxDepth == 0 {
		cfg.MaxDepth = 1
	}
	if cfg.Package == "" && cfg.GoModule != "" {
		cfg.Package = defaultPackage(cfg.GoModule)
	}
	return &Target{cfg: cfg}
}

// defaultPackage derives the root package name from the module path: the last
// segment, suffixed with "ql" unless it already ends in it.
func defaultPackage(goModule string) string {
	p := path.Base(goModule)
	if !strings.HasSuffix(p, "ql") {
		p += "ql"
	}
	return p
}

// Name is the registry/config key (the value users write as target=graphql).
func (t *Target) Name() string { return "graphql" }

// PackageName is the resolved root package name (folder the client is written to).
func (t *Target) PackageName() string { return t.cfg.Package }

// Languages lists the emitters this target ships. Go today; a new language is a
// new sub-emitter added here and dispatched in Generate.
func (t *Target) Languages() []string { return []string{"go"} }

// Generate renders the client from the Model's GraphQL facet in the requested
// language (default Go).
func (t *Target) Generate(_ factory.Ctx, m *coreir.Model, lang string) error {
	if m == nil || m.GraphQL == nil {
		return fmt.Errorf("graphql target: model has no GraphQL schema (wrong source?)")
	}
	if t.cfg.GoModule == "" {
		return fmt.Errorf("graphql target: go_module is required (import path of the generated package)")
	}
	switch lang {
	case "", "go":
		return golang.Generate(golang.Options{
			Schema:        m.GraphQL,
			OutDir:        t.cfg.OutDir,
			Package:       t.cfg.Package,
			GoModule:      t.cfg.GoModule,
			RuntimeModule: t.cfg.RuntimeModule,
			MaxDepth:      t.cfg.MaxDepth,
			Scalars:       t.cfg.Scalars,
			Dialect:       t.cfg.Dialect,
			Version:       t.cfg.Version,
			Sink:          t.cfg.Sink,
		})
	default:
		return fmt.Errorf("graphql target: unsupported language %q (supported: %s)", lang, strings.Join(t.Languages(), ", "))
	}
}
