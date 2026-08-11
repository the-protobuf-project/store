package repository

// iface_view.go prepares the per-schema repository.go view: one interface per
// resource plus its hooks type. Adapters (gorm.go / graphql.go) are emitted by
// their own views; this file owns only the provider-neutral surface.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/provenance"
)

// resourceView is the template data for one resource's interface + hooks.
type resourceView struct {
	Model       string // bare model name, e.g. "Organisation"
	LowerModel  string // camelCase model name, for option struct fields
	PluralField string // Repositories field name, e.g. "Organisations"
	PB          string // qualified proto type, e.g. "orgpbv1.Organisation"
	Parented    bool   // Create/List take a parent resource name
	Pattern     string // resource pattern, for doc comments
}

// schemaPkgView is the template data for one schema's repository.go.
type schemaPkgView struct {
	Header    string
	Package   string
	Imports   []string // fully rendered import lines
	RepoxPkg  string
	Resources []resourceView

	// GraphQL adapter wiring, set when the graphql_module opt is configured.
	HasGraphQL   bool
	ClientPkg    string // client root package name, e.g. "freebusyql"
	GQLResources []gqlResourceView
}

// schemaView builds the repository.go view for schema s, or nil when the
// schema has no repository resources.
func schemaView(pb *pbIndex, db *schema.Database, s *schema.Schema, resources map[*schema.Table]*resource) (*schemaPkgView, error) {
	rs := sortedResources(s, resources)
	if len(rs) == 0 {
		return nil, nil
	}
	pkg := naming.GoPackage(s.Name)
	imports := map[string]string{ // path -> alias ("" = none)
		"context":                       "",
		"gorm.io/gorm":                  "",
		dbGoModule(db) + "/" + repoxPkg: "",
		dbGormModule(db) + "/filterx":   "",
	}
	if m := dbGraphQLModule(db); m != "" {
		imports[m] = clientPkgName(m)
	}
	var views []resourceView
	for _, r := range rs {
		msg, ok := pb.msgs[r.Table.Source.FullName()]
		if !ok {
			return nil, fmt.Errorf("repository: no generated Go type for %s (is its proto in the codegen request?)", r.Table.ProtoMessage)
		}
		pbPkg := string(msg.GoIdent.GoImportPath)
		pkgName := goPackageName(pbPkg)
		imports[pbPkg] = pkgName
		views = append(views, resourceView{
			Model:       r.Table.LocalName,
			LowerModel:  naming.CamelFirst(r.Table.LocalName),
			PluralField: naming.PascalGo(naming.Pluralize(naming.SnakeCase(r.Table.LocalName))),
			PB:          pkgName + "." + msg.GoIdent.GoName,
			Parented:    r.Parent != nil || len(r.Segments) > 1,
			Pattern:     r.Pattern,
		})
	}
	return &schemaPkgView{
		Header: provenance.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Source:        strings.Join(s.SourceProtos(), ", "),
			Database:      db.Name,
			Schema:        s.Name,
			Notes:         []string{"Proto-facing repository interfaces; adapters compose the generated gorm and GraphQL outputs."},
		}),
		Package:   pkg,
		Imports:   renderImports(imports),
		RepoxPkg:  repoxPkg,
		Resources: views,
	}, nil
}

// goPackageName derives the Go package name for a generated pb import path:
// its last segment (protoc-gen-go's `package foo` matches the final ;name or
// directory segment, which the GoIdent import path already reflects).
func goPackageName(path string) string {
	if i := lastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// renderImports renders sorted import lines, aliasing only when the package
// name differs from the path's last segment.
func renderImports(m map[string]string) []string {
	paths := make([]string, 0, len(m))
	for p := range m {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	lines := make([]string, 0, len(paths))
	for _, p := range paths {
		alias := m[p]
		if alias != "" && alias == goPackageName(p) {
			alias = ""
		}
		if alias != "" {
			lines = append(lines, alias+" \""+p+"\"")
		} else {
			lines = append(lines, "\""+p+"\"")
		}
	}
	return lines
}
