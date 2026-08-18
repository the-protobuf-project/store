package gorm

// opts.go reads the gorm target's render-time knobs off the neutral IR. The orm
// backend stamps these onto db.Opts during Enrich (protokit itself never
// interprets them); the gorm target is the only consumer.

import (
	"github.com/the-protobuf-project/protokit/schema"

	"github.com/the-protobuf-project/store/telemetry"
)

// dbGoModule is the Go import path of the generated output directory, needed to
// import each per-schema models package from the migration aggregator. Empty
// when unset (no aggregator is emitted).
func dbGoModule(db *schema.Database) string { return db.Opt("go_module") }

// dbStores reports whether to emit a typed CRUD store per resource.
func dbStores(db *schema.Database) bool { return db.Opt("stores") == "true" }

// dbConverters reports whether to emit proto↔model converters per schema.
func dbConverters(db *schema.Database) bool { return db.Opt("converters") == "true" }

// dbTelemetry reports whether to fold first-party telemetry
// instrumentation into the generated output (instrumented stores, the
// telemetry package, the filterx observer, Registry.Instrument).
func dbTelemetry(db *schema.Database) bool { return telemetry.Enabled(db) }

// dbTelemetryMetrics reports whether instrumented code records op metrics in
// addition to spans (only meaningful when dbTelemetry is true; per-table
// (telemetry.v1.telemetry).metrics narrows it further).
func dbTelemetryMetrics(db *schema.Database) bool { return telemetry.Metrics(db) }

// dbFilters reports whether to emit AIP-160 filter / AIP-132 order_by specs per
// schema plus the shared filterx engine packages.
func dbFilters(db *schema.Database) bool { return db.Opt("filters") == "true" }
