{{.Header}}
{{- if .Extensions}}

-- Extensions (operator classes the search indexes below are built with).
-- Database-wide, not schema-scoped: creating one twice is a no-op.
{{range .Extensions}}CREATE EXTENSION IF NOT EXISTS "{{.}}";
{{end}}
{{- end}}

CREATE SCHEMA IF NOT EXISTS {{.SchemaQ}};
{{range .Enums}}
-- {{.Comment}}
CREATE TYPE {{.TypeRef}} AS ENUM ({{.ValueList}});
{{end}}
{{- range .Tables}}
{{- if .Comment}}
-- {{.Comment}}
{{- end}}
CREATE TABLE {{.Ref}} (
{{- range .Items}}
{{- if .Comment}}
    -- {{.Comment}}
{{- end}}
    {{.Def}}{{if not .Last}},{{end}}
{{- end}}
);
{{range .Indexes}}{{.}}
{{end}}
{{- end}}
{{- if .Comments}}

-- Column and table documentation, persisted to the catalog.
{{range .Comments}}{{.}}
{{end}}{{- end}}
{{- if .Functions}}

-- Auto-update triggers keep updated-at columns current on every UPDATE.
{{range .Functions}}{{.}}
{{end}}
{{range .Triggers}}{{.}}
{{end}}{{- end}}