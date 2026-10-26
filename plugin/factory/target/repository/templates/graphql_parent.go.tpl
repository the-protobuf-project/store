{{.Header}}

package {{.Package}}

import (
{{- range .Imports}}
	{{.}}
{{- end}}
)
{{- range .Lookups}}

// {{.Func}}Key returns the key of the {{.Domain}}.{{.Resource}} row named name,
// and whether there is one.
func {{.Func}}Key(ctx context.Context, svc *{{$.ClientPkg}}.Service, name string) (string, bool, error) {
	row, err := svc.Query.{{.Domain}}.{{.Resource}}.Find(ctx, {{.ResPkg}}.List().Where({{.ResPkg}}.{{.NameField}}.Eq(name)))
	if err != nil {
		return "", false, mapGraphQLErr(err)
	}
	if row == nil {
		return "", false, nil
	}
	return row.{{.KeyField}}, true, nil
}

// {{.Func}}Keys returns the keys of the {{.Domain}}.{{.Resource}} rows parent
// names: one, or every match of an AIP-159 "-" wildcard.
func {{.Func}}Keys(ctx context.Context, svc *{{$.ClientPkg}}.Service, parent string) ([]string, error) {
	match := {{.ResPkg}}.{{.NameField}}.Eq(parent)
	if p, wild := repox.NamePattern(parent); wild {
		match = {{.ResPkg}}.{{.NameField}}.Like(p)
	}
	rows, err := svc.Query.{{.Domain}}.{{.Resource}}.List(ctx, {{.ResPkg}}.List().Where(match))
	if err != nil {
		return nil, mapGraphQLErr(err)
	}
	keys := make([]string, 0, len(rows))
	for i := range rows {
		keys = append(keys, rows[i].{{.KeyField}})
	}
	return keys, nil
}
{{- end}}
