// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package prisma

// scaffold.go builds the non-fragment files that make a generated database
// folder a runnable Prisma project: the datasource schema.prisma view, the
// Prisma 7 <db>.config.ts view, and the static package.json/tsconfig/
// .env.example/.gitignore scaffold. prisma.go keeps the orchestration and
// fragment grouping.

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/protokit/templates"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
	"github.com/the-protobuf-project/store/plugin/factory/provenance"
	"github.com/the-protobuf-project/store/plugin/factory/target/searchindex"
	"github.com/the-protobuf-project/store/plugin/factory/target/types"
)

// schemaFileView prepares the datasource template data for one database.
func schemaFileView(db *schema.Database, provider types.Provider, fx facets.Set) map[string]any {
	names := make([]string, 0, len(db.Schemas))
	quoted := make([]string, 0, len(db.Schemas))
	var extensions []string
	for _, s := range db.Schemas {
		names = append(names, s.Name)
		quoted = append(quoted, `"`+s.Name+`"`)
		for _, t := range s.Tables {
			extensions = append(extensions, searchindex.Extensions(searchindex.For(t, fx))...)
		}
	}
	slices.Sort(extensions)
	extensions = slices.Compact(extensions)
	return map[string]any{
		"Header": provenance.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Database:      db.Name,
			SchemaLabel:   "schemas",
			Schema:        strings.Join(names, ", "),
			Notes:         []string{"Connection URLs live in " + db.Name + ".config.ts (Prisma 7 convention)."},
		}),
		// The datasource block name is just a label (models/client never reference
		// it), so use the database name directly — a valid, self-documenting Prisma
		// identifier — instead of a mangled provider-suffixed form.
		"Datasource":  naming.DatasourceName(db.Name),
		"Provider":    provider.PrismaProvider(),
		"SchemaList":  strings.Join(quoted, ", "),
		"MultiSchema": provider == types.Postgres,
		// Prisma names extensions as bare identifiers, not strings.
		"Extensions":    extensions,
		"ExtensionList": strings.Join(extensions, ", "),
	}
}

// configView prepares the <db>.config.ts template data: the env var carrying
// the connection URL ("bookstore_db" → "BOOKSTORE_DB_DATABASE_URL").
func configView(db *schema.Database) map[string]any {
	names := make([]string, 0, len(db.Schemas))
	for _, s := range db.Schemas {
		names = append(names, s.Name)
	}
	return map[string]any{
		"Header": provenance.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Database:      db.Name,
			SchemaLabel:   "schemas",
			Schema:        strings.Join(names, ", "),
			Notes:         []string{"Prisma 7 configuration; connection URLs are environment-driven."},
		}),
		// Redacted: <db>.config.ts is committed, and the declared url may carry
		// credentials. The env var below is the real connection path.
		"URL":    redactURL(db.URL),
		"EnvVar": envVar(db),
	}
}

// scaffoldFiles maps each scaffold output path suffix to its template name and
// the view that renders it. scaffoldFiles are the static project files; the
// README.md tree is generated separately by writeReadmes (one per folder, with a
// Mermaid ER diagram).
//
// .env is deliberately NOT emitted. Every file here goes through
// p.NewGeneratedFile, which overwrites unconditionally, and a protoc plugin
// cannot read what is already on disk to merge with it. Emitting .env would
// therefore destroy the credentials the user edited into it on the next
// `buf generate`, silently. .env.example is safe to overwrite because it is
// derived entirely from the proto and carries no user edits — it renders the
// provider stub whatever the proto declared, and the generated .gitignore keeps
// it committed while ignoring the .env the user copies it to.
var scaffoldFiles = []struct {
	name, tpl string
	view      func(*schema.Database, types.Provider) map[string]any
}{
	{"package.json", "package.json.tpl", scaffoldView},
	{"tsconfig.json", "tsconfig.json.tpl", scaffoldView},
	{".env.example", "env.example.tpl", envExampleView},
	{".gitignore", "gitignore.tpl", scaffoldView},
}

// writeScaffold emits the package.json, tsconfig.json, .env.example, .gitignore,
// and README.md that turn the database folder into a runnable Prisma project.
func writeScaffold(p *protogen.Plugin, db *schema.Database, provider types.Provider) error {
	for _, sf := range scaffoldFiles {
		f := p.NewGeneratedFile(db.Name+"/"+sf.name, "")
		if err := templates.Render(tmpl, f, sf.tpl, sf.view(db, provider)); err != nil {
			return fmt.Errorf("prisma: %s/%s: %w", db.Name, sf.name, err)
		}
	}
	return nil
}

// DefaultVersion is the Prisma release package.json pins when the
// prisma_version opt names none. The dev dependencies beside it are the
// current majors it supports: TypeScript 5.9 (it needs >= 5.4), Node 24's
// types (it runs on ^20.19, ^22.12 and >= 24), dotenv 18.
const DefaultVersion = "7.10.1"

// version is the Prisma release package.json pins (SetVersion).
var version = DefaultVersion

// SetVersion sets the Prisma release package.json pins, exactly, for prisma
// and @prisma/client: a semantic version ("7.10.1"), which must be one npm
// resolves to a single release, so no range. Empty restores DefaultVersion.
func SetVersion(v string) error {
	if v == "" {
		version = DefaultVersion
		return nil
	}
	if !exactVersion.MatchString(v) {
		return fmt.Errorf("prisma_version %q: want an exact version, e.g. %s", v, DefaultVersion)
	}
	version = v
	return nil
}

// exactVersion matches a semantic version with an optional prerelease.
var exactVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// scaffoldView prepares the template data shared by every scaffold file. It
// deliberately carries no connection URL; the two env views add their own.
func scaffoldView(db *schema.Database, provider types.Provider) map[string]any {
	return map[string]any{
		"Database":      db.Name,
		"PackageName":   strings.ReplaceAll(db.Name, "_", "-") + "-prisma",
		"EnvVar":        envVar(db),
		"ProviderExt":   provider.FragmentExt(),
		"PrismaVersion": version,
	}
}

// envExampleView prepares .env.example's data. Its URL is always the stub — see
// exampleURL.
func envExampleView(db *schema.Database, provider types.Provider) map[string]any {
	v := scaffoldView(db, provider)
	v["URL"] = exampleURL(db, provider)
	return v
}

// envVar derives the connection-URL environment variable name for a database.
func envVar(db *schema.Database) string {
	return strings.ToUpper(db.Name) + "_DATABASE_URL"
}

// exampleURL returns the connection URL written into .env.example: always a
// provider-appropriate stub, never the url declared in the proto.
//
// .env.example is a committed file — the generated .gitignore tells the user to
// keep it. A datasource url with credentials in it would therefore land in
// version control, so the declared url is deliberately not consulted here.
func exampleURL(db *schema.Database, provider types.Provider) string {
	if provider == types.MongoDB {
		return "mongodb://localhost:27017/" + db.Name
	}
	return "postgresql://user:password@localhost:5432/" + db.Name
}

// redactURL strips any userinfo from a connection URL so it can be shown in a
// committed file. The host and database still identify what the proto declared,
// which is the documentation value; the password is what must not be written.
//
// A URL that will not parse is dropped entirely rather than printed on the
// assumption it holds no secret — callers treat "" as "say nothing".
func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	return u.String()
}
