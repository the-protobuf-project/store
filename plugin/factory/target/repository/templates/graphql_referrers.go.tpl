{{.Header}}

package {{.Package}}

import (
{{- range .Imports}}
	{{.}}
{{- end}}
)
{{- range .Referrers}}

// {{.Func}} reports whether a {{.Domain}}.{{.Resource}} row, deleted or not,
// holds key in {{.Table}}: a referrer that keeps the record it names.
func {{.Func}}(ctx context.Context, svc *{{$.ClientPkg}}.Service, key string) (bool, error) {
	row, err := svc.Query.{{.Domain}}.{{.Resource}}.Find(ctx, {{.ResPkg}}.List().Where({{.ResPkg}}.{{.Field}}.Eq(key)))
	if err != nil {
		return false, mapGraphQLErr(err)
	}
	return row != nil, nil
}
{{- end}}
