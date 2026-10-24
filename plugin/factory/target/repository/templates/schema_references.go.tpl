{{.Header}}

package {{.Package}}

// references.go serves this schema's side of store.v1's reference contract
// for its reference fields no foreign key covers: the sources
// store.v1.ReferenceService answers from (ReferrerSources), and a reconciler
// per resource that re-runs the targets' AIP Get over every stored reference.

import (
{{- range .Imports}}
	{{.}}
{{- end}}
)

// ReferrerSources are this schema's external reference fields, read through
// repos, for repox.NewReferenceServer (store.v1.ReferenceService) or
// repox.LocalReferrers. Each lists referrers deleted or not, with the
// field's (store.v1.reference).on_delete.
func ReferrerSources(repos Repositories) []repox.ReferrerSource {
	return []repox.ReferrerSource{
{{- range .Resources}}
{{- $r := .}}
{{- range .ExtRefs}}
		{
			ResourceType: {{printf "%q" $r.Type}},
			Field:        {{printf "%q" .Field}},
			TargetType:   {{printf "%q" .Type}},
			OnDelete:     {{.OnDelete}},
			List: func(ctx context.Context, target string, pageSize int32, pageToken string) ([]string, string, error) {
				rows, next, err := repos.{{$r.PluralField}}.List(ctx, {{if $r.Parented}}{{printf "%q" $r.WildcardParent}}, {{end}}repox.ListInput{
					PageSize:    pageSize,
					PageToken:   pageToken,
					ShowDeleted: true,
					ReadMask:    []string{"name"},
					Where:       []filterx.Condition{ {Field: {{printf "%q" .Field}}, Op: filterx.Op{{if .Repeated}}Has{{else}}Eq{{end}}, Value: target} },
				})
				names := make([]string, 0, len(rows))
				for _, m := range rows {
					names = append(names, m.GetName())
				}
				return names, next, err
			},
		},
{{- end}}
{{- end}}
	}
}
{{- range .Resources}}
{{- if .ExtRefs}}

// Reconcile{{.Model}}References re-runs the AIP Get of every resource a
// {{.Model}} names in a reference no foreign key covers (deleted records
// included), through refs' resolvers, and reports the ones whose target is
// gone. Types with no resolver are counted as unchecked.
func Reconcile{{.Model}}References(ctx context.Context, repo {{.Model}}Repository, refs *repox.References, opts repox.ReconcileOptions) (repox.ReconcileReport, error) {
	return repox.Reconcile(ctx, refs, opts, func(ctx context.Context, pageSize int32, token string) ([]repox.StoredReference, string, error) {
		rows, next, err := repo.List(ctx, {{if .Parented}}{{printf "%q" .WildcardParent}}, {{end}}repox.ListInput{
			PageSize:    pageSize,
			PageToken:   token,
			ShowDeleted: true,
			ReadMask:    []string{"name"{{range .ExtRefs}}, {{printf "%q" .Field}}{{end}}},
		})
		if err != nil {
			return nil, "", err
		}
		var out []repox.StoredReference
		for _, m := range rows {
			{{- range .ExtRefs}}
			{{- if .Repeated}}
			for _, target := range m.{{.Getter}}() {
				out = append(out, repox.StoredReference{Name: m.GetName(), Field: {{printf "%q" .Field}}, ResourceType: {{printf "%q" .Type}}, Target: target})
			}
			{{- else}}
			if target := m.{{.Getter}}(); target != "" {
				out = append(out, repox.StoredReference{Name: m.GetName(), Field: {{printf "%q" .Field}}, ResourceType: {{printf "%q" .Type}}, Target: target})
			}
			{{- end}}
			{{- end}}
		}
		return out, next, nil
	})
}
{{- end}}
{{- end}}
