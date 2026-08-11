package backend

// layout.go is the schema.LayoutResolver half of the seam: the database/schema
// naming policy this plugin resolves from store.yaml, kept deliberately apart from
// the annotation reading in reader.go.
//
// The separation is what makes cross-plugin agreement testable. The same protos
// generated under two different layouts *should* produce different database and
// schema names; the same protos read by two different plugins should not. Only
// the second is a bug, and only a build that keeps config out of the annotation
// path can tell them apart.

import "github.com/the-protobuf-project/protokit/schema"

// Layout resolves proto packages to databases and schemas from store.yaml.
//
// The zero Layout (nil config) has no opinion, which is the correct answer for a
// run with no config file: protokit falls back to its package-path defaults.
type Layout struct{ cfg *Config }

var _ schema.LayoutResolver = Layout{}

// NewLayout returns the Layout backed by cfg. A nil cfg yields a resolver with
// no opinion.
func NewLayout(cfg *Config) Layout { return Layout{cfg: cfg} }

// ResolveDatasource maps a proto package to its database and schema under the
// first matching store.yaml rule.
//
// ok reports whether this resolver has an opinion *at all*, not whether a rule
// matched: a loaded config still applies its global strip_version to packages no
// rule names, so it answers true whenever a config was supplied.
func (l Layout) ResolveDatasource(pkg string) (database, schemaName string, stripVersion, ok bool) {
	if l.cfg == nil {
		return "", "", false, false
	}
	db, s, strip := l.cfg.resolve(pkg)
	return db, s, strip, true
}

// DedupeSchemaTable reports store.yaml's dedupe_schema_table policy (false when no
// config was supplied).
func (l Layout) DedupeSchemaTable() bool {
	return l.cfg != nil && l.cfg.DedupeSchemaTable
}
