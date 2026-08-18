package gorm

// telemetry.go is this target's bridge to the telemetry package. Nothing here
// reads a telemetry.v1 option or knows what the generated adapter contains —
// the target asks what a table's instrumentation is and where the adapter goes,
// and weaves spans into its own store bodies accordingly.
//
// Weaving is deliberately still the target's job: a span inside a generated GORM
// method is that method's rendering. What moved out is everything that is about
// telemetry rather than about GORM.

import (
	"github.com/the-protobuf-project/protokit/schema"

	"github.com/the-protobuf-project/store/telemetry"
)

// telemetryPkg is the package name and output directory of the generated SDK
// adapter, re-exported so the aggregator and store views can name its import
// path without importing the telemetry package for a string.
const telemetryPkg = telemetry.Package

// telemetryModule is the SDK the generated (never the plugin) code imports.
const telemetryModule = telemetry.Module

// tableTelemetry resolves one table's effective instrumentation.
func tableTelemetry(tel telemetry.Set, db *schema.Database, s *schema.Schema, t *schema.Table) (enabled, metrics bool, spanPrefix string) {
	r := tel.Table(db, s, t)
	return r.Enabled, r.Metrics, r.SpanPrefix
}

// telemetryTag renders a column's telemetry struct tag, or "" when the
// table is not instrumented or the field is not labelled.
func telemetryTag(tel telemetry.Set, enabled bool, t *schema.Table, col *schema.Column) string {
	return tel.FieldTag(enabled, t, col)
}
