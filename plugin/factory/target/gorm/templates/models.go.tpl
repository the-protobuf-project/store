{{.Header}}

package {{.Package}}
{{if .Imports}}
{{.Imports}}
{{end}}
{{- range .Enums}}
// {{.Comment}}
type {{.Name}} string

// {{.Name}} values as stored in the database.
const (
{{- range .Values}}
{{- if .Comment}}
	// {{.Comment}}
{{- end}}
	{{.ConstName}} {{.TypeName}} = "{{.MapName}}"
{{- end}}
)
{{end}}
{{- range .Models}}
// {{.Comment}}
type {{.Name}} struct {
{{- range .Fields}}
{{- if .Comment}}
	// {{.Comment}}
{{- end}}
	{{.Decl}}
{{- end}}
}

func (*{{.Name}}) TableName() string { return "{{.TableName}}" }
{{if .Checks}}
// Validate reports every orm.v1.validate preset this {{.Name}} violates, as a
// validatex.Violations naming the offending columns. An absent (nil) optional
// column is not judged — presence is google.api.field_behavior's concern.
// Returns nil when the model is valid.
func (m *{{.Name}}) Validate() error {
	var v validatex.Violations
{{- range .Checks}}
	if !({{.Cond}}) {
		v.Add({{printf "%q" .Field}}, {{printf "%q" .Message}})
	}
{{- end}}
	return v.Err()
}
{{end}}
{{- end}}