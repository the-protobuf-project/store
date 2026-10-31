{{.Header}}

package {{.Package}}

import (
{{- range .Imports}}
	{{.}}
{{- end}}
)
{{- range .Referrers}}

// {{.Func}} reports whether a {{.Domain}}.{{.Resource}} row, deleted or not,
// holds key in {{.Table}}: a referrer that keeps the record it names (a
// purge keeps it; a delete without one is refused).
{{- if .ExceptField}}
// A row whose {{.ExceptField}} is one of except -- removed earlier in the
// same Batch (repox.Unit.RemovedIn) -- does not count.
{{- end}}
func {{.Func}}(ctx context.Context, svc *{{$.ClientPkg}}.Service, key string, except []string) (bool, error) {
	where := {{.ResPkg}}.{{.Field}}.Eq(key)
	{{- if .ExceptField}}
	if len(except) > 0 {
		where = graphql.And(where, graphql.Not({{.ResPkg}}.{{.ExceptField}}.In(except...)))
	}
	{{- end}}
	row, err := svc.Query.{{.Domain}}.{{.Resource}}.Find(ctx, {{.ResPkg}}.List().Where(where))
	if err != nil {
		return false, mapGraphQLErr(err)
	}
	return row != nil, nil
}
{{- end}}
