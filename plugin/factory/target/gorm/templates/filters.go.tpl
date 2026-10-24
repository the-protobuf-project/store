{{.Header}}

package {{.Package}}

import (
	"{{.FilterxImport}}"
)

{{- range .Tables}}

// {{.SpecVar}} is {{.Model}}'s AIP-160 filter / AIP-132 order_by surface,
// consumed by the filterx.Gorm, filterx.Hasura and filterx.OpenSearch engines.
var {{.SpecVar}} = filterx.Spec{
	Table: `{{.Table}}`,
	Fields: map[string]filterx.FieldSpec{
{{- range .Fields}}
		"{{.Field}}": {Column: "{{.Column}}", Kind: filterx.{{.Kind}}{{if .EnumPrefix}}, EnumPrefix: "{{.EnumPrefix}}"{{end}}{{if .ViaTable}}, Via: &filterx.Via{Table: `{{.ViaTable}}`, Key: "{{.ViaKey}}", Column: "{{.ViaColumn}}"}{{end}}{{if .JSONKeys}}, JSONKeys: map[string]filterx.Kind{ {{- range $i, $k := .JSONKeys}}{{if $i}}, {{end}}"{{$k.Path}}": filterx.{{$k.Kind}}{{end -}} }{{end}}},
{{- end}}
	},
{{- if .Search}}
	Search: []string{ {{- range $i, $c := .Search}}{{if $i}}, {{end}}"{{$c}}"{{end -}} },
{{- end}}
	Sort: map[string]filterx.SortSpec{
{{- range .Sort}}
		"{{.Field}}": {Column: "{{.Column}}", Kind: filterx.{{.Kind}}{{if .NotNull}}, NotNull: true{{end}}},
{{- end}}
	},
{{- if .PK}}
	PK: []filterx.SortSpec{ {{- range $i, $c := .PK}}{{if $i}}, {{end}}{Column: "{{$c.Column}}", Kind: filterx.{{$c.Kind}}{{if $c.NotNull}}, NotNull: true{{end}}}{{end -}} },
{{- end}}
}
{{- end}}
