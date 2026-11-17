{{.Header}}
{{- with .Pkg}}{{template "package" .}}{{else}}

//! {{.Doc}}
{{- end}}
{{if .Mods}}
{{range .Mods}}pub mod {{.}};
{{end}}{{end -}}
