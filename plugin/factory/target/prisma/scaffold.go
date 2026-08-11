package prisma

// scaffold.go builds the non-fragment files that make a generated database
// folder a runnable Prisma project: the datasource schema.prisma view, the
// Prisma 7 <db>.config.ts view, and the static package.json/tsconfig/.env/
// .gitignore scaffold. prisma.go keeps the orchestration and fragment grouping.

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/protokit/templates"
	"github.com/the-protobuf-project/store/plugin/factory/target/types"
)

// schemaFileView prepares the datasource template data for one database.
func schemaFileView(db *schema.Database, provider types.Provider) map[string]any {
	names := make([]string, 0, len(db.Schemas))
	quoted := make([]string, 0, len(db.Schemas))
	for _, s := range db.Schemas {
		names = append(names, s.Name)
		quoted = append(quoted, `"`+s.Name+`"`)
	}
	return map[string]any{
		"Header": header.Render("//", header.Info{
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
		"Header": header.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Database:      db.Name,
			SchemaLabel:   "schemas",
			Schema:        strings.Join(names, ", "),
			Notes:         []string{"Prisma 7 configuration; connection URLs are environment-driven."},
		}),
		"URL":    db.URL,
		"EnvVar": envVar(db),
	}
}

// scaffoldFiles maps each scaffold output path suffix to its template name.
// scaffoldFiles are the static project files; the README.md tree is generated
// separately by writeReadmes (one per folder, with a Mermaid ER diagram).
// Both .env.example and a ready-to-use .env are emitted: the datasource url
// declared in the proto pre-populates them, so no copy-and-edit step is needed
// before the prisma scripts run (.env stays git-ignored for local overrides).
var scaffoldFiles = []struct{ name, tpl string }{
	{"package.json", "package.json.tpl"},
	{"tsconfig.json", "tsconfig.json.tpl"},
	{".env.example", "env.example.tpl"},
	{".env", "env.tpl"},
	{".gitignore", "gitignore.tpl"},
}

// writeScaffold emits the package.json, tsconfig.json, .env.example, .gitignore,
// and README.md that turn the database folder into a runnable Prisma project.
func writeScaffold(p *protogen.Plugin, db *schema.Database, provider types.Provider) error {
	view := scaffoldView(db, provider)
	for _, sf := range scaffoldFiles {
		f := p.NewGeneratedFile(db.Name+"/"+sf.name, "")
		if err := templates.Render(tmpl, f, sf.tpl, view); err != nil {
			return fmt.Errorf("prisma: %s/%s: %w", db.Name, sf.name, err)
		}
	}
	return nil
}

// scaffoldView prepares the shared template data for the project scaffold files.
func scaffoldView(db *schema.Database, provider types.Provider) map[string]any {
	return map[string]any{
		"Database":    db.Name,
		"PackageName": strings.ReplaceAll(db.Name, "_", "-") + "-prisma",
		"EnvVar":      envVar(db),
		"URLExample":  exampleURL(db, provider),
		"ProviderExt": provider.FragmentExt(),
	}
}

// envVar derives the connection-URL environment variable name for a database.
func envVar(db *schema.Database) string {
	return strings.ToUpper(db.Name) + "_DATABASE_URL"
}

// exampleURL returns the connection URL written into .env.example: the one
// declared in the proto when present, otherwise a provider-appropriate stub.
func exampleURL(db *schema.Database, provider types.Provider) string {
	if db.URL != "" {
		return db.URL
	}
	if provider == types.MongoDB {
		return "mongodb://localhost:27017/" + db.Name
	}
	return "postgresql://user:password@localhost:5432/" + db.Name
}
