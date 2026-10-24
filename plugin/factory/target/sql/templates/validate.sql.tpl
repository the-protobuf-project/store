{{.Header}}

-- VALIDATE CONSTRAINT checks the rows written before each foreign key below
-- was added NOT VALID by migrate.sql. Rows written since are already checked.
-- A row that still names a missing target fails the statement: fix or remove
-- it, then run this again.
{{range .Validate}}
{{.}}
{{end}}