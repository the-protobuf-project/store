{{.Header}}

BEGIN;
{{if .Extensions}}
-- Extensions (operator classes the search indexes below are built with)
{{range .Extensions}}CREATE EXTENSION IF NOT EXISTS "{{.}}";
{{end}}
{{- end}}
-- Schemas
{{range .Schemas}}CREATE SCHEMA IF NOT EXISTS {{.}};
{{end}}
{{- if .EnumStmts}}
-- Enum types
{{range .EnumStmts}}{{.}}
{{end}}{{- end}}
-- Tables (foreign keys are added after every table exists, so creation order
-- never matters — even across schemas or reference cycles).
{{range .Tables}}
{{- if .Comment}}
-- {{.Comment}}
{{- end}}
CREATE TABLE IF NOT EXISTS {{.Ref}} (
{{- range .Cols}}
{{- if .Comment}}
    -- {{.Comment}}
{{- end}}
    {{.Def}}{{if not .Last}},{{end}}
{{- end}}
);
{{end}}
{{- if .Alters}}
-- Foreign keys
{{range .Alters}}{{.}}
{{end}}{{- end}}
{{- if .Indexes}}
-- Indexes
{{range .Indexes}}{{.}}
{{end}}{{- end}}
{{- if .Comments}}
-- Documentation
{{range .Comments}}{{.}}
{{end}}{{- end}}
{{- if .Functions}}
-- Auto-update triggers
{{range .Functions}}{{.}}
{{end}}
{{range .Triggers}}{{.}}
{{end}}{{- end}}
COMMIT;
