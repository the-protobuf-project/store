{{.Header}}

package {{.Package}}

import (
{{- range .Imports}}
	{{.}}
{{- end}}
)

// {{.Model}}Rows are what a {{.Model}}Writer returns: the {{.Model}} rows to
// store and to remove for the source changes of one write.
type {{.Model}}Rows = repox.ProjectionRows[*{{.PB}}]

// {{.Model}}Writer computes {{.Model}} rows from source changes; register it
// with {{.Var}}.Projector on repox.Conn.Projections.
type {{.Model}}Writer = repox.ProjectionWriter[*{{.PB}}]

// {{.Model}}WriterFunc adapts a function to {{.Model}}Writer.
type {{.Model}}WriterFunc = repox.ProjectionWriterFunc[*{{.PB}}]

// {{.Var}} is the {{.Model}} projection ({{.Table}}): derived from
// {{- range $i, $s := .Sources}}{{if $i}},{{end}} {{$s}}{{end}}, keyed by
// {{- range $i, $k := .Key}}{{if $i}},{{end}} {{$k}}{{end}}.
{{- if .Manual}} Maintained by hand (PROJECTION_MAINTENANCE_MANUAL): Apply
// and Rebuild store rows; no write projects it.{{else}} Kept current by every
// repository write and Batch commit to a source once
// {{.Var}}.Projector(w) is on the connection's Projections.{{end}}
//
//	conn.Projections = repox.NewProjections({{.Var}}.Projector(w))
//	page, next, err := {{.Var}}.Search(ctx, conn, repox.SearchInput{
//		Range:  &repox.Span{Start: from, End: to},
//		Filter: filter, // AIP-160 over {{.Model}}'s fields and JSON paths
//	})
var {{.Var}} = &repox.Projection[*{{.PB}}]{
	Spec: repox.ProjectionSpec{
		Name:      "{{.Name}}",
		Table:     `{{.Table}}`,
		Key:       []string{ {{- range $i, $k := .Key}}{{if $i}}, {{end}}"{{$k}}"{{end -}} },
		KeyColumn:     "id",
		VersionColumn: "row_version",
		Columns: []repox.ProjectionColumn{
{{- range .Columns}}
			{Field: "{{.Field}}", Column: "{{.Column}}", Kind: repox.{{.Kind}}, SQLType: "{{.SQLType}}"{{if .EnumPrefix}}, EnumPrefix: "{{.EnumPrefix}}"{{end}}},
{{- end}}
		},
		Range: "{{.Range}}",
{{- if .IndexLeads}}
		IndexLeads: []string{ {{- range $i, $c := .IndexLeads}}{{if $i}}, {{end}}"{{$c}}"{{end -}} },
{{- end}}
{{- if .Manual}}
		// Maintained by hand: no write projects it.
		Sources: nil,
{{- else}}
		Sources: []string{ {{- range $i, $s := .Sources}}{{if $i}}, {{end}}"{{$s}}"{{end -}} },
{{- end}}
		Filter:  {{.FilterSpec}},
	},
	New: func() *{{.PB}} { return new({{.PB}}) },
{{- if .HasGraphQL}}
	GraphQL: &repox.ProjectionGraphQL{
		List: func(ctx context.Context, conn repox.Conn, req *graphql.ListRequest) ([]map[string]any, error) {
			rows, err := conn.GraphQL.Query.{{.Domain}}.{{.Resource}}.List(ctx, req)
			if err != nil {
				return nil, err
			}
			out := make([]map[string]any, len(rows))
			for i := range rows {
				out[i] = repox.RowMap(&rows[i])
			}
			return out, nil
		},
		Count: func(ctx context.Context, conn repox.Conn, where *graphql.Predicate) (int64, error) {
			req := {{.ResPkg}}.Aggregate()
			if where != nil {
				req = req.Where(*where)
			}
			agg, err := conn.GraphQL.Query.{{.Domain}}.{{.Resource}}.Aggregate(ctx, req)
			if err != nil || agg == nil {
				return 0, err
			}
			return int64(agg.Count), nil
		},
		Insert: func(conn repox.Conn, ops *repox.Ops, row map[string]any) error {
			var ci {{.ResPkg}}.CreateInput
			if err := repox.Remarshal(row, &ci); err != nil {
				return err
			}
			ops.Add(conn.GraphQL.Mutation.{{.Domain}}.{{.Resource}}.CreateOp(ci, new({{.InsertResp}})))
			return nil
		},
		Delete: func(conn repox.Conn, ops *repox.Ops, id string) {
			ops.Add(conn.GraphQL.Mutation.{{.Domain}}.{{.Resource}}.DeleteOp(id, new({{.DeleteResp}})))
		},
	},
{{- end}}
}
