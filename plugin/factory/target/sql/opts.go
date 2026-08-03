package sql

// opts.go reads the sql target's render-time knobs off the neutral IR. The orm
// backend stamps these onto db.Opts during Enrich (protokit itself never
// interprets them).

import "github.com/the-protobuf-project/protokit/schema"

// dbValidationDB reports whether the DB-expressible (orm.v1.validate) presets
// become CHECK constraints in the emitted DDL. The application half of the same
// opt is the gorm target's concern.
func dbValidationDB(db *schema.Database) bool { return db.Opt("validation_db") == "true" }
