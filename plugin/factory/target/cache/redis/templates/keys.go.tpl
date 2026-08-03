{{.Header}}

package {{.Package}}

import "strings"

// The key builders below are generated from each resource's (orm.v1.cache)
// policy. Keys read as the resources they hold — "hotel.bookings/id:01ARZ3…" —
// so a keyspace dump is legible, and every key for a resource sits under its
// collection prefix, which is what lets one call invalidate it.
{{range $r := .Resources}}
// --- {{.Schema}}.{{.Table}} ---

// {{.Singular}}Collection is the prefix every cached entry for a {{.Singular}}
// sits under. Deleting it invalidates the resource entirely.
const {{.Singular}}Collection = "{{.Collection}}"

// {{.Singular}}ListPrefix is where cached {{.Singular}} list results live. The
// "$" segment cannot occur in a resource id, so a list key can never collide
// with a resource key while still sitting under the collection prefix.
const {{.Singular}}ListPrefix = "{{.ListPrefix}}"

// {{.Singular}}TTL is the default lifetime of a cached {{.Singular}}, in seconds;
// 0 means no expiry.
const {{.Singular}}TTL = {{.TTLSeconds}}
{{range .Keys}}
// {{$r.Singular}}{{.GoSuffix}}Key is the key for a {{$r.Singular}} looked up by
// {{.ColumnList}}.
func {{$r.Singular}}{{.GoSuffix}}Key(vals ...any) string { return Key("{{.Name}}", vals...) }
{{end}}
{{- range .Lists}}
// {{$r.Singular}}{{.GoName}}ListKey is the key for the "{{.Name}}" list, filtered
// by {{.ColumnList}}. Its entries expire after {{.TTLSeconds}}s.
func {{$r.Singular}}{{.GoName}}ListKey(parts ...any) string { return ListKey("{{.Prefix}}", parts...) }

// {{$r.Singular}}{{.GoName}}ListTTL is the lifetime of that list, in seconds.
const {{$r.Singular}}{{.GoName}}ListTTL = {{.TTLSeconds}}
{{end}}
{{- end}}
// Resources names every cached resource by its collection prefix, so an operator
// tool can enumerate what this schema caches without reading the annotations.
var Resources = []string{
{{- range .Resources}}
	{{.Singular}}Collection,
{{- end}}
}

// CollectionOf returns the collection prefix a key belongs to, or "" when the key
// belongs to none — useful when reasoning about a keyspace dump.
func CollectionOf(key string) string {
	for _, c := range Resources {
		if strings.HasPrefix(key, c) {
			return c
		}
	}
	return ""
}
