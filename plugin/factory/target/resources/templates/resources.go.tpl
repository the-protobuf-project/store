{{.Header}}
package {{.Package}}

import (
{{- range .Imports}}
	{{if .Alias}}{{.Alias}} {{end}}"{{.Path}}"
{{- end}}
)

// Resources are this database's runtime descriptors, one per proto resource.
//
// They carry no storage engine of their own: a descriptor says which table a
// message maps to, what each column is called and how it is typed, and which
// columns reference other resources. The driver decides what that means.
//
//	reg := store.NewRegistry(Resources...)
//	svc := adapter.New(orm.New(db), reg) // or evm.New(cfg), fabric.New(), ...
//
// Columns whose Generated, AutoCreate or AutoUpdate is set report Managed() at
// runtime: the bridge skips them and the driver supplies the value.
{{- if .Skipped}}
//
// Not described here: {{range $i, $s := .Skipped}}{{if $i}}, {{end}}{{$s}}{{end}}.
// Those tables are synthesized by the generator — outbox tables, many-to-many
// joins — rather than declared by a proto message, so there is no concrete type
// for New to construct. They are still created by the sql and gorm targets.
{{- end}}
var Resources = []store.Resource{
{{- range .Resources}}
	{
{{- if .Comment}}
		// {{.Comment}}
{{- end}}
		Name:     {{printf "%q" .Name}},
		Schema:   {{printf "%q" .Schema}},
		Table:    {{printf "%q" .Table}},
		PKColumn: {{printf "%q" .PKColumn}},
		New:      func() proto.Message { return {{.NewExpr}} },
{{- if .SchemaVersion}}
		SchemaVersion: {{printf "%q" .SchemaVersion}},
{{- end}}
		Columns: []store.Column{
{{- range .Columns}}
			{Name: {{printf "%q" .Name}}, Field: {{printf "%q" .Field}}, Kind: store.{{.Kind}}, SQLType: {{printf "%q" .SQLType}}
			{{- if .PrimaryKey}}, PrimaryKey: true{{end}}
			{{- if .NotNull}}, NotNull: true{{end}}
			{{- if .Unique}}, Unique: true{{end}}
			{{- if .Generated}}, Generated: {{printf "%q" .Generated}}{{end}}
			{{- if .AutoCreate}}, AutoCreate: true{{end}}
			{{- if .AutoUpdate}}, AutoUpdate: true{{end}}},
{{- end}}
		},
{{- if .FKs}}
		FKs: []store.ForeignKey{
{{- range .FKs}}
			{Column: {{printf "%q" .Column}}, ReferencedName: {{printf "%q" .ReferencedName}}, ReferencedField: {{printf "%q" .ReferencedField}}},
{{- end}}
		},
{{- end}}
	},
{{- end}}
}
