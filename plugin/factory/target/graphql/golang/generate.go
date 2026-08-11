package golang

// generate.go is the generation entrypoint and output plumbing: the Options, the
// generator's pass sequence (types → resources → domains → root), and writeFile
// (template execute → gofmt → Sink or OutDir).

import (
	"embed"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"github.com/the-protobuf-project/protokit/graphql/ir"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/store/plugin/factory/target/graphql/golang/selection"
	"github.com/the-protobuf-project/store/plugin/factory/target/graphql/golang/typemap"
)

//go:embed templates/file.go.tmpl
var templatesFS embed.FS

// Options configures Go client generation.
type Options struct {
	Schema        *ir.Schema
	OutDir        string
	Package       string            // root package name (Service + New)
	GoModule      string            // import path of the generated root package
	RuntimeModule string            // import path of the runtime facade
	MaxDepth      int               // relation inlining depth
	Scalars       map[string]string // GraphQL scalar -> Go type overrides
	Dialect       dialect.Dialect   // engine conventions; defaults to dialect.Default()
	Version       string            // plugin version stamped into generated banners

	// Sink, when set, receives each generated file as (path relative to the
	// package root, formatted content) instead of writing to OutDir on disk. The
	// plugin uses it to route output through the protoc response so buf writes the
	// files to the plugin entry's out: directory.
	Sink func(relPath string, content []byte) error
}

// fileData is the data passed to the file template.
type fileData struct {
	Header  string
	Package string
	Imports []string
	Body    string
}

// generator holds shared state across the write_*.go output passes.
type generator struct {
	opts      Options
	tmpl      *template.Template
	r         *renderer
	domains   []*domainGen
	domSchema map[string][]modelGroup // domain -> model groups keyed by owning resource
	ownerOf   map[string]*resGen      // model object name -> the resource that defines it
}

// Generate renders the full Go client into Options.OutDir: the shared type packages,
// per-resource handler packages, per-domain aggregators, and the root Service.
func Generate(opts Options) error {
	tmpl, err := template.ParseFS(templatesFS, "templates/file.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}
	// OrderBy (sort direction) is supplied by the runtime graphql package, so drop it from
	// the generated enums and treat it as a leaf scalar that maps to graphql.OrderBy.
	if _, ok := opts.Schema.Enums["OrderBy"]; ok {
		delete(opts.Schema.Enums, "OrderBy")
		opts.Schema.Scalars["OrderBy"] = true
	}
	if opts.Dialect == nil {
		opts.Dialect = dialect.Default()
	}
	mapper := typemap.New(opts.Schema, opts.Scalars, opts.Dialect)
	g := &generator{
		opts: opts,
		tmpl: tmpl,
		r: &renderer{
			schema:    opts.Schema,
			mapper:    mapper,
			selection: selection.New(opts.Schema, mapper, opts.MaxDepth, qModels),
			dialect:   opts.Dialect,
		},
	}
	g.plan()
	g.domSchema = g.domainObjects()

	if err := g.writeTypes(); err != nil {
		return err
	}
	if err := g.writeResources(); err != nil {
		return err
	}
	if err := g.writeDomains(); err != nil {
		return err
	}
	if err := g.writeHelpers(); err != nil {
		return err
	}
	return g.writeRoot()
}

// writeFile renders one Go file through the template, gofmt-formats it, and emits it at
// <Package>/<subdir>/<name>. Output goes to Options.Sink when set (the plugin routes files
// through the protoc response), otherwise to <OutDir>/<Package>/<subdir>/<name> on disk. The
// whole project is nested under a folder named after the service (the root package), so
// foldername == package == import root.
func (g *generator) writeFile(subdir, name, pkg string, imports []string, body string) error {
	// The banner matches every other target's (protokit header.Render): same
	// stamp format across gorm, prisma, sql, graphql, and repository output.
	h := header.Render("//", header.Info{
		PluginVersion: g.opts.Version,
		Database:      g.opts.Package,
		SchemaLabel:   "package",
		Schema:        pkg,
	})
	var raw strings.Builder
	if err := g.tmpl.Execute(&raw, fileData{Header: h, Package: pkg, Imports: imports, Body: body}); err != nil {
		return fmt.Errorf("template exec for %s: %w", name, err)
	}
	formatted, err := format.Source([]byte(raw.String()))
	if err != nil {
		return fmt.Errorf("gofmt %s/%s: %w", subdir, name, err)
	}
	rel := filepath.Join(g.opts.Package, subdir, name)
	if g.opts.Sink != nil {
		return g.opts.Sink(rel, formatted)
	}
	full := filepath.Join(g.opts.OutDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("failed to create %s: %w", filepath.Dir(full), err)
	}
	return os.WriteFile(full, formatted, 0o644)
}
