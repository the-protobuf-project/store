package entity

// layout.go is the schema.LayoutResolver half of the seam: the database/schema
// naming policy a plugin resolves from its own config file, kept deliberately
// apart from the annotation reading in reader.go.
//
// The separation is what makes cross-plugin agreement testable. The same protos
// generated under two different layouts *should* produce different database and
// schema names; the same protos read by two different plugins should not. Only the
// second is a bug, and only a build that keeps config out of the annotation path
// can tell them apart — which is why golden.IRAgreement compares two plugins under
// one layout rather than comparing layouts.
//
// The resolution lives here rather than in the plugin for the same reason the
// reader does. Two plugins that each implemented "match a package glob, template a
// schema name, strip a trailing version" would agree until the first edge case —
// an empty rule schema, a schema_depth longer than the package, a leaf segment
// that is itself a version. Sharing the code removes the question.

import (
	"strings"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/schema"
)

// LayoutConfig is the naming-policy block of a plugin's config file. It carries
// yaml tags but does no parsing: the plugin owns its config file and unmarshals
// into this, usually by embedding it inline alongside its own keys.
//
//	type Config struct {
//	    entity.LayoutConfig `yaml:",inline"`
//	    Telemetry *telemetryConfig `yaml:"telemetry"`
//	}
//
// Declaring the shape here rather than in each plugin is what keeps "what does
// strip_version apply to?" one answer instead of one per generator.
type LayoutConfig struct {
	// Datasources assigns proto packages to databases and schemas, first match
	// wins.
	Datasources []DatasourceRule `yaml:"datasources"`

	// StripVersion, when true, flattens the API version out of every derived
	// schema name ("bookstore.v1" → schema "bookstore" instead of "bookstore_v1").
	// It applies to resource-type-derived and config-derived schema names, but
	// never to an explicit (entity.v1.datasource).schema annotation. A per-rule
	// strip_version overrides this default for that rule.
	StripVersion bool `yaml:"strip_version"`

	// DedupeSchemaTable, when true, renames a table whose name would stutter with
	// its schema in a schema-qualified identifier ("booking" schema + "bookings"
	// table → "bookingBookings" in tools that join schema+table, e.g. Hasura).
	// The redundant leading schema word is stripped from the table name, or — for
	// the schema's eponymous/primary table, where stripping leaves nothing — the
	// table is renamed to a generic word ("resource", then "entity"/…). Only the
	// generated table name changes; proto/model names are untouched.
	DedupeSchemaTable bool `yaml:"dedupe_schema_table"`
}

// DatasourceRule assigns every proto package matching Match to a database and
// schema. Match is a dotted glob over the package: a trailing "**" matches any
// remaining segments ("fleet.tracking.**"); "*" matches exactly one segment; other
// segments match literally.
type DatasourceRule struct {
	Match    string `yaml:"match"`
	Database string `yaml:"database"`

	// Schema is a literal or a template using {leaf} (the last package segment
	// with a trailing API version dropped). Empty falls back to SchemaDepth.
	Schema string `yaml:"schema"`

	// SchemaDepth, when >0 and Schema is empty, joins the first N package
	// segments with "_" to form the schema name.
	SchemaDepth int `yaml:"schema_depth"`

	// StripVersion overrides the top-level strip_version for packages this rule
	// matches. Nil (the default) inherits the global setting; set it explicitly
	// (true/false) to force version stripping on or off for this datasource.
	StripVersion *bool `yaml:"strip_version"`
}

// Layout returns the schema.LayoutResolver backed by cfg.
//
// A nil cfg yields a resolver with no opinion, which is the correct answer for a
// run with no config file: protokit falls back to its package-path defaults.
// Precedence over the whole chain is unchanged by any of this — an
// (entity.v1.datasource) annotation wins over the config, which wins over the
// package-path default.
func Layout(cfg *LayoutConfig) protokit.LayoutResolver { return layout{cfg: cfg} }

// layout is unexported: a plugin passes the result of [Layout] straight to
// protokit.Build as a schema.LayoutResolver and never names the type.
type layout struct{ cfg *LayoutConfig }

var _ schema.LayoutResolver = layout{}

// ResolveDatasource maps a proto package to its database and schema under the
// first matching rule.
//
// ok reports whether this resolver has an opinion *at all*, not whether a rule
// matched: a loaded config still applies its global strip_version to packages no
// rule names, so it answers true whenever a config was supplied.
func (l layout) ResolveDatasource(pkg string) (database, schemaName string, stripVersion, ok bool) {
	if l.cfg == nil {
		return "", "", false, false
	}
	db, s, strip := l.cfg.resolve(pkg)
	return db, s, strip, true
}

// DedupeSchemaTable reports the config's dedupe_schema_table policy (false when no
// config was supplied).
func (l layout) DedupeSchemaTable() bool {
	return l.cfg != nil && l.cfg.DedupeSchemaTable
}

// resolve returns the database, schema, and version-stripping decision for a
// proto package under the first matching rule, or the global default when no
// rule matches (nil-safe). stripVer reflects the per-rule strip_version when
// set, otherwise the top-level strip_version.
func (c *LayoutConfig) resolve(pkg string) (database, schemaName string, stripVer bool) {
	if c == nil {
		return "", "", false
	}
	segs := strings.Split(pkg, ".")
	for _, r := range c.Datasources {
		if matchPackage(r.Match, segs) {
			sv := c.StripVersion
			if r.StripVersion != nil {
				sv = *r.StripVersion
			}
			return r.Database, ruleSchema(r, segs), sv
		}
	}
	return "", "", c.StripVersion
}

// ruleSchema computes the schema name a matched rule assigns to a package.
func ruleSchema(r DatasourceRule, segs []string) string {
	switch {
	case r.Schema != "":
		return strings.ReplaceAll(r.Schema, "{leaf}", leafSegment(segs))
	case r.SchemaDepth > 0 && r.SchemaDepth <= len(segs):
		return strings.Join(segs[:r.SchemaDepth], "_")
	default:
		return ""
	}
}

// leafSegment returns the last package segment, dropping a trailing API version
// ("store.apps.calendar.v1" → "calendar").
func leafSegment(segs []string) string {
	i := len(segs) - 1
	if i > 0 && isVersionSegment(segs[i]) {
		i--
	}
	if i < 0 {
		return ""
	}
	return segs[i]
}

// isVersionSegment reports whether seg is an API version segment like "v1",
// "v2", "v1alpha1", or "v1beta1": a 'v' followed by a digit.
func isVersionSegment(seg string) bool {
	return len(seg) >= 2 && seg[0] == 'v' && seg[1] >= '0' && seg[1] <= '9'
}

// matchPackage reports whether the dotted glob pattern matches package segments.
func matchPackage(pattern string, segs []string) bool {
	pats := strings.Split(pattern, ".")
	for i, p := range pats {
		if p == "**" {
			return true // trailing wildcard: the rest matches
		}
		if i >= len(segs) {
			return false
		}
		if p != "*" && p != segs[i] {
			return false
		}
	}
	return len(pats) == len(segs)
}
