{{.Header}}

package {{.Package}}

import (
{{- range .Imports}}
	{{.}}
{{- end}}
)

{{- range .Resources}}

// apply{{.Model}}Mask merges the masked fields of in onto merged. An empty
// mask replaces every mutable field; identity, parentage, timestamps, and etag
// are repository-managed and never masked. Message-typed fields are replaced
// wholesale when the mask touches them or any of their subpaths, and a oneof is
// replaced whole when the mask touches any of its arms — it holds at most one.
func apply{{.Model}}Mask(merged, in *{{.PB}}, paths []string) {
	{{- range .MaskFields}}
	{{- if .Oneof}}
	if {{range $i, $p := .Paths}}{{if $i}} || {{end}}repox.GroupTouched(paths, "{{$p}}"){{end}} {
		merged.{{.Oneof}} = in.{{.Oneof}}
	}
	{{- else if .Message}}
	if repox.GroupTouched(paths, "{{.Path}}") {
		merged.{{.GoField}} = in.Get{{.GoField}}()
	}
	{{- else}}
	if repox.InMask(paths, "{{.Path}}") {
		merged.{{.GoField}} = in.Get{{.GoField}}()
	}
	{{- end}}
	{{- end}}
	{{- range .VOMaskLines}}
	{{.}}
	{{- end}}
}
{{- end}}
