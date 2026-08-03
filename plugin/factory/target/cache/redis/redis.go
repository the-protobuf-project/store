// Package redis is the cache-redis target: it renders the neutral cache Plan
// into a Redis-backed cache package.
//
// The provider is the target, not a field on an annotation. Selecting
// target=cache-redis is what puts a Redis client in your build; a schema that
// wants Valkey or a third-party cache selects a different target and gets a
// different client, with the same key builders and the same Cache interface. A
// provider you would rather supply yourself is one Cache implementation away —
// nothing generated depends on this client except the client itself.
//
// The Plan this renders (see the parent package) mentions no language. Adding a
// second language here is a second template set over the same Plan, not a second
// derivation of what should be cached.
package redis

import (
	"bytes"
	"embed"
	"fmt"
	"go/format"
	"io"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/orm/plugin/factory/target/cache"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/protokit/templates"
)

//go:embed templates/*.tpl
var templateFS embed.FS

var tmpl = templates.MustParse(templateFS, "templates/*.tpl")

// pkg is the package name and output directory of the generated cache.
const pkg = "cache"

// provider names the cache product this target renders a client for. It appears
// in the generated documentation so a reader can tell which target produced the
// tree.
const provider = "Redis"

// clientModule is the import path of the Redis wrapper the generated client is
// built on. Keeping it a single constant is what makes swapping the underlying
// client a one-line change here rather than an edit spread through a template —
// the same shape the graphql target uses for its runtime dependency.
const clientModule = "github.com/redis/go-redis/v9"

// Generator implements schema.Target for the Redis cache backend.
type Generator struct{}

// Name returns the target identifier used in buf.gen.yaml opt: [target=cache-redis].
func (g *Generator) Name() string { return "cache-redis" }

// Generate writes the cache package for every database that caches anything.
// A tree where no resource declares an (orm.v1.cache) policy produces no files
// at all, so selecting this target costs nothing until a schema uses it.
func (g *Generator) Generate(p *protogen.Plugin, dbs []*schema.Database) error {
	for _, db := range dbs {
		plan := cache.Build(db)
		if plan.Empty() {
			continue
		}
		v := view(db, plan)
		for _, f := range []struct{ path, tpl string }{
			{pkg + "/cache.go", "cachex.go.tpl"},
			{pkg + "/keys.go", "keys.go.tpl"},
			{pkg + "/redis.go", "client.go.tpl"},
		} {
			gf := p.NewGeneratedFile(f.path, "")
			if err := renderGo(gf, f.tpl, v); err != nil {
				return fmt.Errorf("cache-redis: %s: %w", f.path, err)
			}
		}
	}
	return nil
}

// resourceView adds the language-specific names a Go template needs on top of
// the neutral plan. This is where — and the only place where — Go naming
// conventions enter: the Plan itself carries none, so another language's
// template set derives its own from the same neutral fields.
type resourceView struct {
	cache.Resource
	Keys  []keyView
	Lists []listView
}

type keyView struct {
	cache.KeySpec
	GoSuffix   string // "Id" — the exported name fragment
	ColumnList string // "hotel_id, check_in" — for the doc comment
}

type listView struct {
	cache.ListSpec
	GoName     string
	ColumnList string
}

// view assembles the template data for one database's cache package.
func view(db *schema.Database, plan cache.Plan) map[string]any {
	rs := make([]resourceView, 0, len(plan.Resources))
	for _, r := range plan.Resources {
		rv := resourceView{Resource: r}
		for _, k := range r.Keys {
			rv.Keys = append(rv.Keys, keyView{
				KeySpec:    k,
				GoSuffix:   naming.PascalGo(k.Suffix),
				ColumnList: strings.Join(k.Columns, ", "),
			})
		}
		for _, l := range r.Lists {
			rv.Lists = append(rv.Lists, listView{
				ListSpec:   l,
				GoName:     naming.PascalGo(l.Name),
				ColumnList: columnsOrAll(l.Columns),
			})
		}
		rs = append(rs, rv)
	}
	return map[string]any{
		"Header": header.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Database:      db.Name,
			SchemaLabel:   "package",
			Schema:        pkg,
			Notes: []string{
				"Cache keys and a " + provider + " client, generated from each resource's (orm.v1.cache) policy.",
			},
		}),
		"Package":      pkg,
		"Provider":     provider,
		"ClientModule": clientModule,
		"Resources":    rs,
	}
}

// columnsOrAll describes a list's filter columns, naming the unfiltered case
// rather than rendering an empty list into a doc comment.
func columnsOrAll(cols []string) string {
	if len(cols) == 0 {
		return "no filter (the whole collection)"
	}
	return strings.Join(cols, ", ")
}

// renderGo executes a template and gofmts the result, so a malformed template
// fails here rather than in the consumer's build.
func renderGo(w io.Writer, name string, data any) error {
	var buf bytes.Buffer
	if err := templates.Render(tmpl, &buf, name, data); err != nil {
		return err
	}
	out, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("gofmt %s: %w\nrendered source:\n%s", name, err, buf.String())
	}
	_, err = w.Write(out)
	return err
}
