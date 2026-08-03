// Package nats is the stream-nats target: it renders the neutral cache Plan's
// stream coordinates into a NATS JetStream consumer that invalidates a cache.
//
// The broker is the target, as the cache provider is: selecting
// target=stream-nats is what puts a NATS client in your build. The handler and
// subject table it emits are broker-neutral — written against an Event — so
// moving to another broker replaces one file.
//
// Only resources whose policy asks for stream invalidation appear here, so a
// schema that invalidates on write alone produces nothing.
package nats

import (
	"bytes"
	"embed"
	"fmt"
	"go/format"
	"io"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/orm/plugin/factory/target/cache"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/protokit/templates"
)

//go:embed templates/*.tpl
var templateFS embed.FS

var tmpl = templates.MustParse(templateFS, "templates/*.tpl")

const (
	pkg      = "stream"
	provider = "NATS JetStream"
)

// Generator implements schema.Target for the NATS stream backend.
type Generator struct{}

// Name returns the target identifier used in buf.gen.yaml opt: [target=stream-nats].
func (g *Generator) Name() string { return "stream-nats" }

// Generate writes the stream package for every database with a stream-invalidated
// resource.
func (g *Generator) Generate(p *protogen.Plugin, dbs []*schema.Database) error {
	for _, db := range dbs {
		streamed := withStream(cache.Build(db))
		if len(streamed) == 0 {
			continue
		}
		v := view(db, streamed)
		for _, f := range []struct{ path, tpl string }{
			{pkg + "/stream.go", "subjects.go.tpl"},
			{pkg + "/nats.go", "client.go.tpl"},
		} {
			gf := p.NewGeneratedFile(f.path, "")
			if err := renderGo(gf, f.tpl, v); err != nil {
				return fmt.Errorf("stream-nats: %s: %w", f.path, err)
			}
		}
	}
	return nil
}

// withStream keeps only the resources that actually declare a subject. A policy
// that invalidates on write needs no consumer, and emitting an empty subject for
// it would produce a consumer subscribing to nothing.
func withStream(plan cache.Plan) []cache.Resource {
	var out []cache.Resource
	for _, r := range plan.Resources {
		if r.Stream.Subject != "" {
			out = append(out, r)
		}
	}
	return out
}

func view(db *schema.Database, rs []cache.Resource) map[string]any {
	return map[string]any{
		"Header": header.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Database:      db.Name,
			SchemaLabel:   "package",
			Schema:        pkg,
			Notes: []string{
				"Change-event subjects and the cache invalidation they drive, for " + provider + ".",
			},
		}),
		"Package":   pkg,
		"Provider":  provider,
		"Resources": rs,
	}
}

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
