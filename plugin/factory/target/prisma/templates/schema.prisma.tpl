{{.Header}}

datasource {{.Datasource}} {
  provider = "{{.Provider}}"
{{- if .MultiSchema}}
  schemas  = [{{.SchemaList}}]
{{- end}}
{{- if .Extensions}}
  // Operator classes the search indexes are built with. Declared here so
  // `prisma migrate` creates the extension before the index that needs it.
  extensions = [{{.ExtensionList}}]
{{- end}}
}

generator client {
  provider = "prisma-client"
  output   = "./generated/client"
{{- if .Extensions}}
  // postgresqlExtensions is what lets the datasource declare extensions above;
  // it is still a Prisma preview feature. Emitted only because this schema has a
  // search index needing an operator class from one.
  previewFeatures = ["postgresqlExtensions"]
{{- end}}
}
