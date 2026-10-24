{{.Header}}

package {{.Package}}

import (
{{- range .Imports}}
	{{.}}
{{- end}}
)

// {{.Var}} is the {{.Model}} projection ({{.Table}}) copied into OpenSearch
// under the alias "{{.Alias}}": documents keyed by the projection's key,
// versioned by its row_version, mapped by {{.MappingVar}}.
//
//	idx, err := {{.Var}}.Ensure(ctx, client) // template, first index, alias
//	ixr := {{.Var}}.Indexer(client, osx.IndexerOptions{Queue: 1024})
//	conn.Projections.Feed = ixr // every committed projection write
//	page, next, err := {{.Var}}.Search(ctx, client, repox.SearchInput{Range: ..., Filter: ...})
var {{.Var}} = osx.NewIndex({{.Projection}}, osx.IndexSpec{
	Alias:   "{{.Alias}}",
	Mapping: {{.MappingVar}},
})

// {{.MappingVar}} is {{.Model}}'s document mapping, generated from its
// columns.
const {{.MappingVar}} = `{{.Mapping}}`
