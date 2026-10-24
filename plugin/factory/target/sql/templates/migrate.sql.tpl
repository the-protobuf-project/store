{{.Header}}

BEGIN;
{{if .Extensions}}
-- Extensions (operator classes the indexes and constraints below are built with)
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
{{- if .Constraints}}
-- Exclusion constraints ((store.v1.table).no_overlap): no two rows sharing a
-- key may cover overlapping time.
{{range .Constraints}}{{.}}
{{end}}{{- end}}
{{- if .External}}
-- Foreign keys into tables of other databases on this server (or found only
-- by resource type). Constraint only: added NOT VALID when missing and when
-- the target table exists, so this file applies in any order with theirs.
-- validate.sql checks the rows that predate them.
{{range .External}}{{.}}
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
