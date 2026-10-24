{{.Header}}

// Package {{.Package}} wires every model generated for the {{.Database}} database
// into one factory Registry, so an application can migrate the whole database in
// a single call. Default is preloaded with every generated model; Register adds
// your own.
package {{.Package}}

import (
{{- range .Imports}}
	{{if .Alias}}{{.Alias}} {{end}}"{{.Path}}"
{{- end}}

{{if .Constraints}}	"errors"
{{end}}{{if .External}}	"fmt"
	"log/slog"
{{end}}	"gorm.io/gorm"
{{- if .Telemetry}}
	"{{.TelemetryImport}}"
{{- end}}
)

// Migrator is the subset of *gorm.DB the Migrate call needs; *gorm.DB satisfies
// it. EnsureSchemas and Instrument take a concrete *gorm.DB, since creating
// schemas and installing plugins need the full driver.
type Migrator interface {
	AutoMigrate(models ...any) error
}

// schemas lists every Postgres schema the registered models live in, so
// EnsureSchemas can create them before AutoMigrate builds schema-qualified tables.
var schemas = []string{
{{- range .Schemas}}
	"{{.}}",
{{- end}}
}
{{- if .Extensions}}

// extensions lists the Postgres extensions the models' search indexes are built
// with. AutoMigrate cannot CREATE EXTENSION, and an index naming an operator
// class from a missing one fails, so EnsureSchemas installs them first.
// Installing an extension needs privileges a least-privilege application role
// may not hold; grant it, or create them once out of band with the SQL target's
// migrate.sql, which issues the same statements.
var extensions = []string{
{{- range .Extensions}}
	"{{.}}",
{{- end}}
}
{{- end}}

{{- if .Constraints}}

// constraints are the exclusion constraints the models declare with
// (store.v1.table).no_overlap: no two rows sharing a key may cover overlapping
// time. AutoMigrate cannot express EXCLUDE, so Migrate adds them after it. Each
// is idempotent, and needs the btree_gist extension EnsureSchemas installs.
var constraints = []string{
{{- range .Constraints}}
	`{{.}}`,
{{- end}}
}
{{- end}}

{{- if .External}}

// externalForeignKey is one foreign key into a table of another database on
// this server (or one found only by its resource type): a constraint, never an
// association.
type externalForeignKey struct {
	name, table, target string
	add, validate       string
}

// externalForeignKeys are this database's constraint-only foreign keys, which
// MigrateExternal adds and ValidateExternal validates.
var externalForeignKeys = []externalForeignKey{
{{- range .External}}
	{
		name:   "{{.Name}}",
		table:  `{{.Table}}`,
		target: `{{.Target}}`,
		add: `{{.Add}}`,
		validate: `{{.Validate}}`,
	},
{{- end}}
}
{{- end}}

// Registry collects GORM models so they migrate together.
type Registry struct {
	models []any
}

// New returns an empty Registry.
func New() *Registry { return &Registry{} }

// Register appends models to the registry and returns it for chaining.
func (r *Registry) Register(models ...any) *Registry {
	r.models = append(r.models, models...)
	return r
}

// Models returns every registered model, in registration order.
func (r *Registry) Models() []any { return r.models }

{{- if .Constraints}}
// Migrate runs AutoMigrate for every registered model, then adds the
// exclusion constraints AutoMigrate cannot, which takes a *gorm.DB.
func (r *Registry) Migrate(db Migrator) error {
	if err := db.AutoMigrate(r.models...); err != nil {
		return err
	}
	exec, ok := db.(interface{ Exec(string, ...any) *gorm.DB })
	if !ok {
		return errors.New("migrate: the no_overlap constraints need a *gorm.DB to run DDL")
	}
	for _, c := range constraints {
		if err := exec.Exec(c).Error; err != nil {
			return err
		}
	}
	return nil
}
{{- else}}
// Migrate runs AutoMigrate for every registered model.
func (r *Registry) Migrate(db Migrator) error {
	return db.AutoMigrate(r.models...)
}
{{- end}}

{{- if .External}}
// MigrateExternal adds this database's foreign keys into tables of other
// databases on the same server: a constraint only, NOT VALID, and only when it
// is missing. Run it after every database's Migrate, since its target is
// another registry's table; a target that does not exist yet is skipped with
// a log line and picked up on the next start. Safe to run on every start.
//
//	for _, r := range []*Registry{a.Default, b.Default} { r.Migrate(db) }
//	a.Default.MigrateExternal(db)
//	b.Default.MigrateExternal(db)
func (*Registry) MigrateExternal(db *gorm.DB) error {
	for _, fk := range externalForeignKeys {
		var present bool
		if err := db.Raw("SELECT to_regclass(?) IS NOT NULL", fk.target).Scan(&present).Error; err != nil {
			return fmt.Errorf("migrate external %s: %w", fk.name, err)
		}
		if !present {
			slog.Warn("store: skipping foreign key; its target table does not exist yet",
				"constraint", fk.name, "table", fk.table, "target", fk.target)
			continue
		}
		if err := db.Exec(fk.add).Error; err != nil {
			return fmt.Errorf("migrate external %s: %w", fk.name, err)
		}
	}
	return nil
}

// ValidateExternal validates the constraints MigrateExternal added NOT VALID:
// it checks the rows written before each was added (later rows are checked
// already). A row naming a missing target fails it; fix or remove the row and
// run it again. Each constraint is validated once; an already-valid or
// missing one is skipped.
func (*Registry) ValidateExternal(db *gorm.DB) error {
	for _, fk := range externalForeignKeys {
		if err := db.Exec(fk.validate).Error; err != nil {
			return fmt.Errorf("validate external %s: %w", fk.name, err)
		}
	}
	return nil
}
{{- else}}
// MigrateExternal adds this database's foreign keys into other databases'
// tables after every registry's Migrate. This database has none, so it does
// nothing; it exists so startup code can call it on every registry alike.
func (*Registry) MigrateExternal(*gorm.DB) error { return nil }

// ValidateExternal validates what MigrateExternal added. Nothing here.
func (*Registry) ValidateExternal(*gorm.DB) error { return nil }
{{- end}}

// EnsureSchemas creates every Postgres schema the registered models live in, if
// it does not already exist. AutoMigrate does not create schemas, so call this
// before Migrate when models map to schema-qualified tables:
//
//	if err := {{.Package}}.Default.EnsureSchemas(db); err != nil {
//		log.Fatal(err)
//	}
//	if err := {{.Package}}.Default.Migrate(db); err != nil {
//		log.Fatal(err)
//	}
func (*Registry) EnsureSchemas(db *gorm.DB) error {
	for _, name := range schemas {
		if err := db.Exec(`CREATE SCHEMA IF NOT EXISTS "` + name + `"`).Error; err != nil {
			return err
		}
	}
{{- if .Extensions}}
	for _, name := range extensions {
		if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS "` + name + `"`).Error; err != nil {
			return err
		}
	}
{{- end}}
	return nil
}

// Default holds every model generated for the {{.Database}} database. Attach it
// in your application's startup:
//
//	if err := {{.Package}}.Default.Migrate(db); err != nil {
//		log.Fatal(err)
//	}
//
// Register your own models alongside the generated ones:
//
//	{{.Package}}.Default.Register(&MyModel{})
var Default = New().Register(
{{range .Models}}	&{{.}}{},
{{end}})
{{- if .Telemetry}}

// Instrument installs the generated first-party telemetry GORM plugin on
// db, so every query the application runs emits a span{{if .TelemetryMetrics}} and metric{{end}} through o.
// Call it once at startup, after opening the connection and before serving
// traffic:
//
//	o, err := telemetry.Build().WithService("api", "1.0.0").WithOTLP("localhost", 4317).WithTracing().Build()
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer o.Close()
//	if err := {{.Package}}.Default.Instrument(db, o); err != nil {
//		log.Fatal(err)
//	}
func (*Registry) Instrument(db *gorm.DB, o *telemetry.Handle) error {
	return db.Use(telemetry.Plugin(o))
}
{{- end}}
