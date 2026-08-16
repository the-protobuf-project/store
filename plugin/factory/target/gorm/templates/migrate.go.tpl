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

	"gorm.io/gorm"
{{- if .Telemetry}}
	"{{.OpentelementryImport}}"
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

// Migrate runs AutoMigrate for every registered model.
func (r *Registry) Migrate(db Migrator) error {
	return db.AutoMigrate(r.models...)
}

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

// Instrument installs the generated first-party opentelementry GORM plugin on
// db, so every query the application runs emits a span{{if .TelemetryMetrics}} and metric{{end}} through o.
// Call it once at startup, after opening the connection and before serving
// traffic:
//
//	o, err := opentelementry.New().WithService("api", "1.0.0").WithOTLP("localhost", 4317).WithTracing().Build()
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer o.Close()
//	if err := {{.Package}}.Default.Instrument(db, o); err != nil {
//		log.Fatal(err)
//	}
func (*Registry) Instrument(db *gorm.DB, o *opentelementry.Opentelementry) error {
	return db.Use(telemetry.Plugin(o))
}
{{- end}}
