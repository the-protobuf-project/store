// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package repository generates provider-agnostic, proto-facing repository
// layers from the same proto-derived schema the database targets render: one
// CRUD interface per resource (speaking proto messages and AIP conventions —
// resource names, page tokens, field masks, etags) plus adapters composing the
// generated gorm output (models, stores, converters, filterx) and, when a
// client module is configured, the generated GraphQL client. Custom logic
// layers on through hooks and adapter embedding, never by editing output.
package repository

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"go/format"
	"io"
	"sort"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/protokit/templates"
	"github.com/the-protobuf-project/store/ddn/ddnrefs"
	"github.com/the-protobuf-project/store/plugin/factory/provenance"
	"github.com/the-protobuf-project/store/plugin/factory/target/gopkg"
)

//go:embed templates/*.tpl
var templateFS embed.FS

var tmpl = templates.MustParse(templateFS, "templates/*.tpl")

// repoxImport is the shared repository runtime, runtime-go's database/repox.
const repoxImport = "github.com/the-protobuf-project/runtime-go/database/repox"

// repoxgrpcPkg is the generated package holding the gRPC side of the
// reference contract: it is built on store's own storepbv1 stubs, which
// runtime-go cannot import, so it stays generated beside the repositories.
const repoxgrpcPkg = "repoxgrpc"

// Generator implements schema.Target for the repository layer. Lang selects
// the language: "" or "go" composes the gorm output (Generate), "rust" binds
// tpp-database's typed repository (see rust.go).
type Generator struct {
	Lang string
}

// Name returns the target identifier used in buf.gen.yaml opt: [target=repository].
func (g *Generator) Name() string { return "repository" }

// Languages lists the languages this target emits; the first is the default.
func (g *Generator) Languages() []string { return []string{"go", "rust"} }

// ForLanguage returns the generator bound to lang, as the lang opt selects it.
func (g *Generator) ForLanguage(lang string) schema.Target { return &Generator{Lang: lang} }

// GenerateIR renders lang=rust from the whole IR, which it needs for the
// store.v1 column facets (SQL type overrides); Go renders from the databases
// alone, as it always has.
func (g *Generator) GenerateIR(p *protogen.Plugin, ir *protokit.IR) error {
	if g.Lang == "rust" {
		return g.generateRust(p, ir)
	}
	return g.Generate(p, ir.Databases)
}

// opts accessors (stamped by the store backend; see backend.WithRepositoryModules).
func dbGoModule(db *schema.Database) string      { return db.Opt("go_module") }
func dbGormModule(db *schema.Database) string    { return db.Opt("gorm_module") }
func dbGraphQLModule(db *schema.Database) string { return db.Opt("graphql_module") }

// Generate writes one package per schema holding the repository interfaces
// and their adapters, over runtime-go's database/repox.
func (g *Generator) Generate(p *protogen.Plugin, dbs []*schema.Database) error {
	if g.Lang == "rust" {
		return g.generateRust(p, &protokit.IR{Databases: dbs})
	}
	pb := newPbIndex(p)
	rev := referrerIndex(dbs)
	// The gRPC binding of the reference contract (repoxgrpc/grpc.go) is emitted
	// only when something uses it: an external reference, or a
	// reference_services: entry. It brings google.golang.org/grpc and
	// store.v1's stubs into the generated code.
	var (
		needGRPC  bool
		endpoints []referenceService
		grpcDB    *schema.Database
		// The DDN reference plugin's config (ddn/references.json), emitted
		// with the GraphQL adapters.
		ddnDB   *schema.Database
		ddnMuts []ddnrefs.Mutation
	)
	for _, db := range dbs {
		if dbGoModule(db) == "" || dbGormModule(db) == "" {
			return fmt.Errorf("repository: database %q needs the go_module and gorm_module opts "+
				"(import paths of the repository output dir and the generated gorm output)", db.Name)
		}
		resources, err := planResources(pb, db)
		if err != nil {
			return err
		}
		planReferences(pb, resources, rev)
		if len(resources) == 0 {
			continue
		}
		if grpcDB == nil {
			grpcDB = db
		}
		eps, err := referenceServices(db)
		if err != nil {
			return err
		}
		if len(eps) > 0 && len(endpoints) == 0 {
			endpoints = eps
		}
		for _, r := range resources {
			if len(r.ExtRefs) > 0 {
				needGRPC = true
			}
		}
		for _, s := range db.Schemas {
			projections, err := projectionViews(pb, db, s, naming.GoPackage(s.Name))
			if err != nil {
				return err
			}
			for _, pv := range projections {
				name := fmt.Sprintf("%s/%s/projection_%s.go", gopkg.Dir(db.Name), pv.Package, naming.SnakeCase(pv.Model))
				if err := renderGo(p.NewGeneratedFile(name, ""), "projection.go.tpl", pv); err != nil {
					return fmt.Errorf("repository: %s: %w", name, err)
				}
			}
			view, err := schemaView(pb, db, s, resources)
			if err != nil {
				return err
			}
			if view == nil {
				continue
			}
			gormViews, err := gormResourceViews(pb, db, s, resources, view.Resources)
			if err != nil {
				return err
			}
			pkg := naming.GoPackage(s.Name)
			files := map[string]struct {
				tpl  string
				view any
			}{
				"repository.go": {"repository.go.tpl", view},
				"names.go":      {"names.go.tpl", namesView(db, s, pkg, gormViews)},
				"mask.go":       {"mask.go.tpl", maskView(pb, db, s, pkg, gormViews)},
				"gorm.go":       {"gorm.go.tpl", gormFileView(pb, db, s, pkg, gormViews)},
			}
			if view.HasExtRefs {
				files["references.go"] = struct {
					tpl  string
					view any
				}{"schema_references.go.tpl", map[string]any{
					"Header":    fileHeader(db, s, "Reference integrity across databases and services: ReferenceService sources and reconcilers."),
					"Package":   pkg,
					"Imports":   renderImports(map[string]string{"context": "", repoxImport: "", "github.com/the-protobuf-project/runtime-go/database/filterx": ""}),
					"Resources": view.Resources,
				}}
			}
			if dbGraphQLModule(db) != "" {
				ddnDB = db
				ddnMuts = append(ddnMuts, ddnMutations(s, sortedResources(s, resources), db.Opt("graphql_root_field_prefix"))...)
				gqlViews, needs, err := gqlResourceViews(pb, db, s, resources, gormViews)
				if err != nil {
					return err
				}
				voConvs := gqlVOConvs(pb, db, s, resources, &needs)
				voConvs = gqlNodeConvs(pb, db, s, resources, voConvs, &needs)
				view.HasGraphQL = true
				view.ClientPkg = clientPkgName(dbGraphQLModule(db))
				view.GQLResources = gqlViews
				files["graphql.go"] = struct {
					tpl  string
					view any
				}{"graphql.go.tpl", graphqlFileView(pb, db, s, pkg, gqlViews)}
				// A nested ancestor's lookups, in a file per ancestor schema: the
				// only file importing that schema's client package.
				for name, view := range graphqlParentViews(db, s, pkg, gqlViews) {
					files[name] = struct {
						tpl  string
						view any
					}{"graphql_parent.go.tpl", view}
				}
				// The referrers a purge asks about, likewise in a file per
				// referring schema.
				for name, view := range graphqlReferrerViews(db, s, pkg, gqlViews) {
					files[name] = struct {
						tpl  string
						view any
					}{"graphql_referrers.go.tpl", view}
				}
				// protobuf.go mirrors the gorm output's converter file name: the
				// proto↔row/input mappers for the graphql side.
				files["protobuf.go"] = struct {
					tpl  string
					view any
				}{"graphql_protobuf.tpl", graphqlConvertView(pb, db, s, pkg, gqlViews, voConvs, needs)}
			}
			for name, render := range files {
				f := p.NewGeneratedFile(fmt.Sprintf("%s/%s/%s", gopkg.Dir(db.Name), pkg, name), "")
				if err := renderGo(f, render.tpl, render.view); err != nil {
					return fmt.Errorf("repository: %s/%s/%s: %w", db.Name, pkg, name, err)
				}
			}
		}
	}
	if ddnDB != nil {
		cfg := ddnrefs.Config{Version: ddnrefs.Version, Mutations: ddnMuts, Endpoints: ddnEndpoints(endpoints)}
		b, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		if _, err := p.NewGeneratedFile("ddn/references.json", "").Write(append(b, '\n')); err != nil {
			return err
		}
		if _, err := p.NewGeneratedFile("ddn/references-plugin.hml", "").Write([]byte(ddnHookMetadata)); err != nil {
			return err
		}
	}
	if grpcDB != nil && (needGRPC || len(endpoints) > 0) {
		view := repoxgrpcView(grpcDB)
		view["Endpoints"] = endpoints
		f := p.NewGeneratedFile(repoxgrpcPkg+"/grpc.go", "")
		if err := renderGo(f, "grpc.go.tpl", view); err != nil {
			return fmt.Errorf("repository: %s/grpc.go: %w", repoxgrpcPkg, err)
		}
	}
	return nil
}

// repoxgrpcView prepares the view of the reference contract's gRPC binding.
func repoxgrpcView(db *schema.Database) map[string]any {
	return map[string]any{
		"Header": provenance.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Database:      db.Name,
			SchemaLabel:   "package",
			Schema:        repoxgrpcPkg,
			Notes:         []string{"gRPC binding of the reference contract, over runtime-go's database/repox."},
		}),
	}
}

// clientPkgName is the package name of the generated GraphQL client: the last
// segment of its module path (the graphql target names package == directory).
func clientPkgName(module string) string {
	if module == "" {
		return ""
	}
	return gopkg.LastSegment(module)
}

// pbIndex resolves proto message descriptors to protogen messages so the
// generated interfaces name the exact generated Go types, and carries the
// package name protoc-gen-go declared at each of their import paths so those
// types are qualified as the .pb.go files spell them.
type pbIndex struct {
	msgs  map[protoreflect.FullName]*protogen.Message
	names gopkg.Names
}

func newPbIndex(p *protogen.Plugin) *pbIndex {
	idx := &pbIndex{
		msgs:  map[protoreflect.FullName]*protogen.Message{},
		names: gopkg.Index(p),
	}
	var walk func(msgs []*protogen.Message)
	walk = func(msgs []*protogen.Message) {
		for _, m := range msgs {
			idx.msgs[m.Desc.FullName()] = m
			walk(m.Messages)
		}
	}
	for _, f := range p.Files {
		walk(f.Messages)
	}
	return idx
}

// sortedResources returns s's repository resources in table order.
func sortedResources(s *schema.Schema, resources map[*schema.Table]*resource) []*resource {
	var out []*resource
	for _, t := range s.Tables {
		if r, ok := resources[t]; ok {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Table.LocalName < out[j].Table.LocalName })
	return out
}

// renderGo executes the named template, gofmt-formats the result, and writes it.
func renderGo(w io.Writer, name string, data any) error {
	var buf bytes.Buffer
	if err := templates.Render(tmpl, &buf, name, data); err != nil {
		return err
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("gofmt %s: %w\nrendered source:\n%s", name, err, buf.String())
	}
	_, err = w.Write(formatted)
	return err
}
