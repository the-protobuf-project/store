// Package backend is orm's schema.Backend: the bridge between protokit's
// generic IR builder and orm's own annotation package. protokit reads the generic
// google.api.* structure itself and calls this backend for the rest — the three
// Read* methods map orm.v1 options onto protokit's neutral structure during the
// build, and Enrich folds orm's database rendering (types, sizing, indexes,
// constraints) into the IR afterward. This is what lets a user annotate entirely
// with orm.v1 while protokit imports no orm proto.
package backend

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/orm/plugin/factory/target/cache"
	"github.com/the-protobuf-project/orm/plugin/factory/target/validate"
	"github.com/the-protobuf-project/orm/plugin/pb/ormpbv1"
	"github.com/the-protobuf-project/protokit/schema"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Backend implements schema.Backend for orm's orm.v1 annotations. It also owns
// orm's grouping config (orm.yaml) and the render-time knobs the gorm target
// reads — protokit holds none of these; the Backend resolves them here and folds
// them into the neutral IR (a resolved Datasource per file, db.Opts per database).
type Backend struct {
	cfg        *Config // orm.yaml layout config; nil when none was supplied
	goModule   string  // Go import path of the output dir (gorm migration aggregator)
	stores     bool    // gorm: also emit a typed CRUD store per resource
	telemetry  bool    // gorm: fold first-party opentelementry instrumentation into the output
	converters bool    // gorm: also emit proto↔model converters per schema
	filters    bool    // gorm: also emit AIP filter/order specs + the filterx engines

	// gormModule / graphqlModule are the repository target's knobs: the import
	// paths of the generated gorm output and the generated GraphQL client the
	// repository adapters compose. Empty graphqlModule means gorm-only
	// repositories (the open-source posture).
	gormModule    string
	graphqlModule string

	// validation / validationDB are the two halves of orm.v1.validate
	// enforcement: the application checks (validatex + Validate methods) and the
	// DDL CHECK constraints. Both follow the validation opt, and orm.yaml's
	// validation: block can disable either half independently.
	validation   bool
	validationDB bool
}

// New builds an orm Backend from the resolved plugin options. The zero value
// (Backend{}) is still valid — no config, no gorm aggregator — which is all the
// non-gorm targets need.
func New(cfg *Config, goModule string, stores, telemetry, converters, filters bool) Backend {
	return Backend{cfg: cfg, goModule: goModule, stores: stores, telemetry: telemetry, converters: converters, filters: filters}
}

// WithRepositoryModules returns a copy of b carrying the repository target's
// module paths (see the gorm_module / graphql_module plugin opts).
func (b Backend) WithRepositoryModules(gormModule, graphqlModule string) Backend {
	b.gormModule, b.graphqlModule = gormModule, graphqlModule
	return b
}

// WithValidation returns a copy of b carrying the validation opt. Both halves —
// the application checks and the DDL CHECK constraints — start from it; orm.yaml
// narrows them in Enrich. A chainable setter rather than another New parameter:
// New already takes four positional bools, and a fifth and sixth would be easy
// to transpose at the call site.
func (b Backend) WithValidation(on bool) Backend {
	b.validation, b.validationDB = on, on
	return b
}

// ReadDatasource resolves the file's grouping from orm.v1.datasource and orm.yaml,
// returning a fully-resolved protokit Datasource. Precedence for database/schema:
// annotation > config > protokit's package-path default (applied by protokit when
// both are empty). An explicit datasource.schema annotation is authoritative and
// never version-stripped; a config-derived (or resource-type-derived) schema obeys
// strip_version, signalled to protokit via SchemaStrip.
func (b Backend) ReadDatasource(d protoreflect.FileDescriptor) schema.Datasource {
	o := datasourceOpts(d)
	cfgDB, cfgSchema, stripVer := b.cfg.resolve(string(d.Package()))

	database := o.GetDatabase()
	if database == "" {
		database = cfgDB
	}

	schemaName := o.GetSchema()
	strip := false
	if schemaName == "" {
		schemaName = cfgSchema
		strip = stripVer
	}

	return schema.Datasource{
		Database:    database,
		Schema:      schemaName,
		Provider:    o.GetProvider(),
		URL:         o.GetUrl(),
		SchemaStrip: strip,
	}
}

// DedupeSchemaTable reports orm.yaml's dedupe_schema_table policy (false when no
// config was supplied).
func (b Backend) DedupeSchemaTable() bool {
	return b.cfg != nil && b.cfg.DedupeSchemaTable
}

// ReadTable maps orm.v1.table's structural fields onto protokit's TableStructure.
// The rendering field (indexes) is applied later in Enrich.
func (Backend) ReadTable(d protoreflect.MessageDescriptor) schema.TableStructure {
	o := tableOpts(d)
	return schema.TableStructure{
		Table:      o.GetTable(),
		Skip:       o.GetSkip(),
		ID:         idStrategy(o.GetId()),
		Timestamps: o.GetTimestamps(),
	}
}

// ReadColumn maps orm.v1.column's structural fields onto protokit's
// ColumnStructure. The rendering fields (type, unique, index, …) are applied
// later in Enrich.
func (Backend) ReadColumn(d protoreflect.FieldDescriptor) schema.ColumnStructure {
	o := columnOpts(d)
	return schema.ColumnStructure{
		Column:   o.GetColumn(),
		Skip:     o.GetSkip(),
		OnDelete: refAction(o.GetOnDelete()),
		OnUpdate: refAction(o.GetOnUpdate()),
	}
}

// Enrich folds orm.v1 rendering options into the built IR: per-table composite
// indexes and per-column type/sizing overrides, unique/index constraints, and
// default expressions. It reads each column's and table's Source descriptor (the
// generic provenance handle protokit stamps on); a synthesized column or table
// (nil Source) carries no annotation and is left as protokit built it.
func (b Backend) Enrich(dbs []*schema.Database) error {
	// Telemetry default: the telemetry plugin opt, then orm.yaml's telemetry:
	// block overrides. Metrics and logs default on when telemetry itself is on.
	telOn, telMetrics, telLogs := b.telemetry, true, true
	if b.cfg != nil && b.cfg.Telemetry != nil {
		if b.cfg.Telemetry.Enabled != nil {
			telOn = *b.cfg.Telemetry.Enabled
		}
		if b.cfg.Telemetry.Metrics != nil {
			telMetrics = *b.cfg.Telemetry.Metrics
		}
		if b.cfg.Telemetry.Logs != nil {
			telLogs = *b.cfg.Telemetry.Logs
		}
	}

	// Validation default: the validation plugin opt, then orm.yaml's validation:
	// block overrides. Both halves default on when validation itself is on, so a
	// bare validation opt gives defence in depth (app checks plus constraints).
	valOn, valApp, valDB := b.validation, true, b.validationDB
	if b.cfg != nil && b.cfg.Validation != nil {
		if b.cfg.Validation.Enabled != nil {
			valOn = *b.cfg.Validation.Enabled
			valDB = valOn
		}
		if b.cfg.Validation.App != nil {
			valApp = *b.cfg.Validation.App
		}
		if b.cfg.Validation.DB != nil {
			valDB = *b.cfg.Validation.DB
		}
	}

	// An annotation that cannot mean anything is a hard error, not a warning:
	// unlike an unresolved reference (which degrades to a soft FK), there is no
	// fallback — a preset that does not fit its column, or a cache key naming a
	// column that does not exist, is simply dropped, leaving the author believing
	// a guarantee holds when nothing implements it. Every offender is collected so
	// one build reports them all.
	var bad []string

	for _, db := range dbs {
		for _, s := range db.Schemas {
			for _, t := range s.Tables {
				for _, c := range t.Columns {
					for _, msg := range validate.Verify(c) {
						bad = append(bad, fmt.Sprintf("table %q column %q: %s", t.Name, c.Name, msg))
					}
				}
				for _, msg := range cache.Verify(t) {
					bad = append(bad, fmt.Sprintf("table %q: %s", t.Name, msg))
				}
				for _, idx := range tableOptsMsg(t.Source).GetIndexes() {
					t.Indexes = append(t.Indexes, &schema.Index{
						Name: idx.GetIndex(), Columns: idx.GetColumns(), Unique: idx.GetUnique(),
					})
				}
				for _, c := range t.Columns {
					enrichColumn(c)
				}
			}
		}
		// Stamp the gorm target's render-time knobs onto the neutral IR. protokit
		// never interprets these; the gorm target reads them back off db.Opts.
		if db.Opts == nil {
			db.Opts = map[string]string{}
		}
		db.Opts["go_module"] = b.goModule
		db.Opts["stores"] = boolStr(b.stores)
		db.Opts["converters"] = boolStr(b.converters)
		db.Opts["telemetry"] = boolStr(telOn)
		db.Opts["telemetry_metrics"] = boolStr(telMetrics)
		db.Opts["telemetry_logs"] = boolStr(telLogs)
		db.Opts["filters"] = boolStr(b.filters)
		db.Opts["validation"] = boolStr(valOn && valApp)
		db.Opts["validation_db"] = boolStr(valOn && valDB)
		db.Opts["gorm_module"] = b.gormModule
		db.Opts["graphql_module"] = b.graphqlModule
	}
	if len(bad) > 0 {
		return fmt.Errorf("orm: %d annotation problem(s):\n  - %s",
			len(bad), strings.Join(bad, "\n  - "))
	}
	return nil
}

// boolStr renders a bool as the "true"/"false" tokens db.Opts stores.
func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// enrichColumn folds orm.v1.column's constraint rendering onto one column:
// unique/index are additive, and a non-empty default_value overrides the AIP enum
// default protokit may have set. The column's SQL type is not stored on the IR —
// the type/max_length/precision override is read at render time by
// types.SQLForColumn off the column's Source descriptor.
func enrichColumn(c *schema.Column) {
	o := columnOptsField(c.Source)
	if v := o.GetDefaultValue(); v != "" {
		c.Default = v
	}
	if o.GetUnique() {
		c.Unique = true
	}
	if o.GetIndex() {
		c.Index = true
	}
}

// idStrategy maps orm.v1.IdStrategy onto protokit's neutral schema.IDStrategy.
func idStrategy(s ormpbv1.IdStrategy) schema.IDStrategy {
	switch s {
	case ormpbv1.IdStrategy_ID_STRATEGY_ULID:
		return schema.IDULID
	case ormpbv1.IdStrategy_ID_STRATEGY_UUID:
		return schema.IDUUID
	default:
		return schema.IDUnspecified
	}
}

// refAction converts an orm.v1.ReferentialAction to its SQL clause form.
func refAction(a ormpbv1.ReferentialAction) string {
	switch a {
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_CASCADE:
		return "CASCADE"
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_RESTRICT:
		return "RESTRICT"
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_SET_NULL:
		return "SET NULL"
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_SET_DEFAULT:
		return "SET DEFAULT"
	case ormpbv1.ReferentialAction_REFERENTIAL_ACTION_NO_ACTION:
		return "NO ACTION"
	default:
		return ""
	}
}

// --- option accessors (safe empty value when the extension or descriptor is absent) ---

func datasourceOpts(d protoreflect.FileDescriptor) *ormpbv1.DatasourceOptions {
	if d == nil || !proto.HasExtension(d.Options(), ormpbv1.E_Datasource) {
		return &ormpbv1.DatasourceOptions{}
	}
	return proto.GetExtension(d.Options(), ormpbv1.E_Datasource).(*ormpbv1.DatasourceOptions)
}

func tableOpts(d protoreflect.MessageDescriptor) *ormpbv1.TableOptions { return tableOptsMsg(d) }

func tableOptsMsg(d protoreflect.MessageDescriptor) *ormpbv1.TableOptions {
	if d == nil || !proto.HasExtension(d.Options(), ormpbv1.E_Table) {
		return &ormpbv1.TableOptions{}
	}
	return proto.GetExtension(d.Options(), ormpbv1.E_Table).(*ormpbv1.TableOptions)
}

func columnOpts(d protoreflect.FieldDescriptor) *ormpbv1.ColumnOptions { return columnOptsField(d) }

func columnOptsField(d protoreflect.FieldDescriptor) *ormpbv1.ColumnOptions {
	if d == nil || !proto.HasExtension(d.Options(), ormpbv1.E_Column) {
		return &ormpbv1.ColumnOptions{}
	}
	return proto.GetExtension(d.Options(), ormpbv1.E_Column).(*ormpbv1.ColumnOptions)
}
