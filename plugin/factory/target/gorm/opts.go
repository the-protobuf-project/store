package gorm

// opts.go reads the gorm target's render-time knobs off the neutral IR. The orm
// backend stamps these onto db.Opts during Enrich (protokit itself never
// interprets them); the gorm target is the only consumer.

import "github.com/the-protobuf-project/protokit/schema"

// dbGoModule is the Go import path of the generated output directory, needed to
// import each per-schema models package from the migration aggregator. Empty
// when unset (no aggregator is emitted).
func dbGoModule(db *schema.Database) string { return db.Opt("go_module") }

// dbStores reports whether to emit a typed CRUD store per resource.
func dbStores(db *schema.Database) bool { return db.Opt("stores") == "true" }

// dbConverters reports whether to emit proto↔model converters per schema.
func dbConverters(db *schema.Database) bool { return db.Opt("converters") == "true" }

// dbTelemetry reports whether to fold first-party opentelementry
// instrumentation into the generated output (instrumented stores, the
// telemetry package, the filterx observer, Registry.Instrument).
func dbTelemetry(db *schema.Database) bool { return db.Opt("telemetry") == "true" }

// dbTelemetryMetrics reports whether instrumented code records op metrics in
// addition to spans (only meaningful when dbTelemetry is true; per-table
// (telemetry.v1.telemetry).metrics narrows it further).
func dbTelemetryMetrics(db *schema.Database) bool { return db.Opt("telemetry_metrics") == "true" }

// dbTelemetryLogs reports whether the telemetry adapter logs failed
// operations (only meaningful when dbTelemetry is true).
func dbTelemetryLogs(db *schema.Database) bool { return db.Opt("telemetry_logs") == "true" }

// dbFilters reports whether to emit AIP-160 filter / AIP-132 order_by specs per
// schema plus the shared filterx engine packages.
func dbFilters(db *schema.Database) bool { return db.Opt("filters") == "true" }

// dbValidation reports whether to fold orm.v1.validate enforcement into the
// generated output (the validatex runtime, per-model Validate methods, and the
// store write-path calls).
func dbValidation(db *schema.Database) bool { return db.Opt("validation") == "true" }

// dbValidationDB reports whether the DB-expressible presets also become CHECK
// constraints (only meaningful when dbValidation is true).
func dbValidationDB(db *schema.Database) bool { return db.Opt("validation_db") == "true" }

// (the cachex runtime, the stores' Cache field, and the invalidation on write).
func dbCache(db *schema.Database) bool { return db.Opt("cache") == "true" }
