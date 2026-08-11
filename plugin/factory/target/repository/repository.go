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
	"fmt"
	"go/format"
	"io"
	"sort"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/protokit/templates"
	"github.com/the-protobuf-project/store/plugin/factory/provenance"
)

//go:embed templates/*.tpl
var templateFS embed.FS

var tmpl = templates.MustParse(templateFS, "templates/*.tpl")

// repoxPkg is the shared runtime package name and directory.
const repoxPkg = "repox"

// Generator implements schema.Target for the repository layer.
type Generator struct{}

// Name returns the target identifier used in buf.gen.yaml opt: [target=repository].
func (g *Generator) Name() string { return "repository" }

// opts accessors (stamped by the store backend; see backend.WithRepositoryModules).
func dbGoModule(db *schema.Database) string      { return db.Opt("go_module") }
func dbGormModule(db *schema.Database) string    { return db.Opt("gorm_module") }
func dbGraphQLModule(db *schema.Database) string { return db.Opt("graphql_module") }

// Generate writes the shared repox runtime once, then one package per schema
// holding the repository interfaces and their adapters.
func (g *Generator) Generate(p *protogen.Plugin, dbs []*schema.Database) error {
	repoxEmitted := false
	pb := newPbIndex(p)
	for _, db := range dbs {
		if dbGoModule(db) == "" || dbGormModule(db) == "" {
			return fmt.Errorf("repository: database %q needs the go_module and gorm_module opts "+
				"(import paths of the repository output dir and the generated gorm output)", db.Name)
		}
		resources, err := planResources(pb, db)
		if err != nil {
			return err
		}
		if len(resources) == 0 {
			continue
		}
		if !repoxEmitted {
			f := p.NewGeneratedFile(repoxPkg+"/"+repoxPkg+".go", "")
			if err := renderGo(f, "repox.go.tpl", repoxView(db)); err != nil {
				return fmt.Errorf("repository: %s: %w", repoxPkg, err)
			}
			repoxEmitted = true
		}
		for _, s := range db.Schemas {
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
			if dbGraphQLModule(db) != "" {
				gqlViews, needs, err := gqlResourceViews(pb, db, s, resources, gormViews)
				if err != nil {
					return err
				}
				voConvs := gqlVOConvs(pb, db, s, resources, &needs)
				view.HasGraphQL = true
				view.ClientPkg = clientPkgName(dbGraphQLModule(db))
				view.GQLResources = gqlViews
				files["graphql.go"] = struct {
					tpl  string
					view any
				}{"graphql.go.tpl", graphqlFileView(pb, db, s, pkg, gqlViews)}
				// protobuf.go mirrors the gorm output's converter file name: the
				// proto↔row/input mappers for the graphql side.
				files["protobuf.go"] = struct {
					tpl  string
					view any
				}{"graphql_protobuf.tpl", graphqlConvertView(pb, db, s, pkg, gqlViews, voConvs, needs)}
			}
			for name, render := range files {
				f := p.NewGeneratedFile(fmt.Sprintf("%s/%s/%s", db.Name, pkg, name), "")
				if err := renderGo(f, render.tpl, render.view); err != nil {
					return fmt.Errorf("repository: %s/%s/%s: %w", db.Name, pkg, name, err)
				}
			}
		}
	}
	return nil
}

// repoxView prepares the shared runtime view. The GraphQL client handle is
// only present when a client module is configured.
func repoxView(db *schema.Database) map[string]any {
	return map[string]any{
		"Header": provenance.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Database:      db.Name,
			SchemaLabel:   "package",
			Schema:        repoxPkg,
			Notes:         []string{"Shared runtime for the generated proto-facing repositories."},
		}),
		"FilterxImport": dbGormModule(db) + "/filterx",
		"GraphQLModule": dbGraphQLModule(db),
		"GraphQLPkg":    clientPkgName(dbGraphQLModule(db)),
	}
}

// clientPkgName is the package name of the generated GraphQL client: the last
// segment of its module path (the graphql target names package == directory).
func clientPkgName(module string) string {
	if module == "" {
		return ""
	}
	if i := lastIndexByte(module, '/'); i >= 0 {
		return module[i+1:]
	}
	return module
}

func lastIndexByte(s string, b byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// pbIndex resolves proto message descriptors to protogen messages so the
// generated interfaces name the exact generated Go types.
type pbIndex struct {
	msgs map[protoreflect.FullName]*protogen.Message
}

func newPbIndex(p *protogen.Plugin) *pbIndex {
	idx := &pbIndex{msgs: map[protoreflect.FullName]*protogen.Message{}}
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
