package backend

// layout.go is where this plugin hands protokit the two things that are not its
// own vocabulary: the naming policy resolved from store.yaml, and the full reader
// set for a run.
//
// Both are thin on purpose. The naming policy *rules* live in entity, not here —
// see [NewLayout] — and the reader set is a list rather than logic. What matters
// is that there is exactly one of each, so plugin dispatch, the golden harness,
// and the agreement tests cannot assemble different builds.

import (
	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/store/plugin/entity"
	telemetrygen "github.com/the-protobuf-project/store/telemetry"
)

// NewLayout returns the schema.LayoutResolver backed by cfg. A nil cfg yields a
// resolver with no opinion, which is the correct answer for a run with no config
// file: protokit falls back to its package-path defaults.
//
// The resolution itself is entity's. store.yaml keeps its shape — the keys are
// still `datasources:`, `strip_version:`, `dedupe_schema_table:` — but a package
// glob, a {leaf} template, and a stripped version suffix mean the same thing here
// as in any other plugin reading entity.v1, because it is the same code deciding.
// That is the half of cross-plugin agreement golden.IRAgreement cannot check: it
// compares two plugins under one layout, so a layout that disagreed with itself
// across plugins would pass.
func NewLayout(cfg *Config) protokit.LayoutResolver {
	if cfg == nil {
		return entity.Layout(nil)
	}
	return entity.Layout(&cfg.LayoutConfig)
}

// Readers returns the complete reader set for one store build, in the order
// protokit sorts them into anyway:
//
//	entity.v1         the neutral names every protokit plugin must agree on
//	entity.v1-compat  the deprecated orm.v1 / store.v1 structural options
//	store.v1          this plugin's physical vocabulary (r)
//	telemetry.v1      tracing/metrics annotations
//
// The order is what makes the precedence readable rather than incidental: an
// explicit entity.v1 annotation beats a legacy one on the same node, and protokit
// reports the loser as a lint diagnostic rather than dropping it. Nothing here
// depends on registration order — protokit sorts by Key — but writing them in
// resolved order means the list reads the way the build behaves.
//
// Assembling the set in one place is the point. A test that built its readers by
// hand would be testing a plugin this binary never assembles, and the reader most
// likely to be forgotten is the compat one, whose absence shows up only as
// somebody else's pre-migration proto silently losing its structure.
func Readers(r Reader) []protokit.FacetReader {
	return []protokit.FacetReader{
		entity.Reader(),
		entity.CompatReader(),
		r,
		telemetrygen.NewReader(),
	}
}
