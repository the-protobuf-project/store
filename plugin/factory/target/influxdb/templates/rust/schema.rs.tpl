{{.Header}}

//! The `{{.Schema}}` schema of `{{.Database}}`: its models and one store per table.

{{range .Files}}mod {{.}};
{{end}}
{{range .Files}}pub use {{.}}::*;
{{end -}}
