// Package backend is this plugin's bridge to protokit: the reader that brings
// the orm.v1 annotation package into protokit's IR, and the layout policy
// resolved from orm.yaml.
//
// protokit v1.2.0 replaced the old schema.Backend — which conflated reading a
// generator's annotations, deciding neutral names, and applying a config file —
// with three separate seams, and this package supplies all three:
//
//   - [Reader] is a schema.FacetReader: it attaches orm.v1 options to each node
//     as a facet (see factory/facets), so any target can read them back and no
//     other plugin can mutate them.
//   - [Reader] is also a schema.StructureReader, for the narrow set of options
//     protokit must act on *while* building rather than read afterward.
//   - [Layout] is a schema.LayoutResolver: the database/schema naming policy
//     that comes from orm.yaml rather than from the protos.
//
// Keeping the annotation and the config apart is the point of the split. The
// annotation travels with the proto and must mean the same thing to every
// plugin; the config travels with the deployment and may legitimately differ.
package backend

import (
	"github.com/the-protobuf-project/orm/plugin/factory/facets"
	"github.com/the-protobuf-project/orm/plugin/pb/ormpbv1"
	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/schema"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Reader brings orm.v1 into the build. It reads the vocabulary into facets,
// supplies the structure protokit consumes mid-build, and folds this plugin's
// render-time knobs onto each database in Enrich.
//
// It carries the plugin opts (not the layout config — that is [Layout]'s) plus
// orm.yaml's telemetry block, which tunes the gorm target rather than naming
// anything.
type Reader struct {
	cfg        *Config // orm.yaml; read here only for its telemetry block
	goModule   string  // Go import path of the output dir (gorm migration aggregator)
	stores     bool    // gorm: also emit a typed CRUD store per resource
	telemetry  bool    // gorm: fold first-party opentelementry instrumentation in
	converters bool    // gorm: also emit proto↔model converters per schema
	filters    bool    // gorm: also emit AIP filter/order specs + the filterx engines

	// gormModule / graphqlModule are the repository target's knobs: the import
	// paths of the generated gorm output and the generated GraphQL client the
	// repository adapters compose. Empty graphqlModule means gorm-only
	// repositories (the open-source posture).
	gormModule    string
	graphqlModule string
}

// Compile-time proof that Reader satisfies all three halves of the seam.
var (
	_ schema.FacetReader     = Reader{}
	_ schema.StructureReader = Reader{}
	_ schema.Enricher        = Reader{}
)

// New builds a Reader from the resolved plugin options. The zero value
// (Reader{}) is still valid — no config, no gorm aggregator — which is all the
// non-gorm targets need.
func New(cfg *Config, goModule string, stores, telemetry, converters, filters bool) Reader {
	return Reader{cfg: cfg, goModule: goModule, stores: stores, telemetry: telemetry, converters: converters, filters: filters}
}

// WithRepositoryModules returns a copy of r carrying the repository target's
// module paths (see the gorm_module / graphql_module plugin opts).
func (r Reader) WithRepositoryModules(gormModule, graphqlModule string) Reader {
	r.gormModule, r.graphqlModule = gormModule, graphqlModule
	return r
}

// --- schema.FacetReader ---

// Key namespaces this reader's facets. It names the vocabulary, not the plugin.
func (Reader) Key() string { return facets.Key }

// ReadFile attaches the file's orm.v1.datasource options, or nothing when the
// file carries none.
func (Reader) ReadFile(d protoreflect.FileDescriptor) (any, error) {
	if d == nil || !proto.HasExtension(d.Options(), ormpbv1.E_Datasource) {
		return nil, nil
	}
	return datasourceOpts(d), nil
}

// ReadMessage attaches the message's orm.v1.table options, or nothing.
func (Reader) ReadMessage(d protoreflect.MessageDescriptor) (any, error) {
	if d == nil || !proto.HasExtension(d.Options(), ormpbv1.E_Table) {
		return nil, nil
	}
	return tableOpts(d), nil
}

// ReadField attaches the field's orm.v1.column and orm.v1.query options as one
// [facets.Field]. protokit stores one value per node, so a field carrying both
// annotations needs them bundled. Returns nothing when the field carries
// neither.
func (Reader) ReadField(d protoreflect.FieldDescriptor) (any, error) {
	if d == nil {
		return nil, nil
	}
	hasCol := proto.HasExtension(d.Options(), ormpbv1.E_Column)
	hasQuery := proto.HasExtension(d.Options(), ormpbv1.E_Query)
	if !hasCol && !hasQuery {
		return nil, nil
	}
	f := &facets.Field{}
	if hasCol {
		f.Column = columnOpts(d)
	}
	if hasQuery {
		f.Query = queryOpts(d)
	}
	return f, nil
}

// --- schema.StructureReader ---
//
// These supply the structure protokit acts on while building. protokit consults
// them only where its own protokit.v1 read produced nothing, so an author who
// has migrated a file to protokit.v1 sees these ignored.

// ReadDatasource maps orm.v1.datasource onto protokit's neutral Datasource.
//
// It returns the *annotation only*. Merging orm.yaml in here is what the old
// Backend did, and it is why two plugins over one proto could disagree about a
// schema name; the config now arrives separately through [Layout], which
// protokit consults after every structure reader. SchemaStrip stays false
// because an explicitly annotated schema is authoritative and never stripped.
func (Reader) ReadDatasource(d protoreflect.FileDescriptor) schema.Datasource {
	o := datasourceOpts(d)
	return schema.Datasource{
		Database: o.GetDatabase(),
		Schema:   o.GetSchema(),
		Provider: o.GetProvider(),
		URL:      o.GetUrl(),
	}
}

// ReadTable maps orm.v1.table onto protokit's TableStructure, composite indexes
// included: protokit appends declared indexes before it synthesizes foreign-key
// indexes, so an index that already covers an FK column suppresses the redundant
// single-column one.
func (Reader) ReadTable(d protoreflect.MessageDescriptor) schema.TableStructure {
	o := tableOpts(d)
	return schema.TableStructure{
		Table:      o.GetTable(),
		Skip:       o.GetSkip(),
		ID:         idStrategy(o.GetId()),
		Timestamps: o.GetTimestamps(),
		Indexes:    indexes(o.GetIndexes()),
	}
}

// ReadColumn maps orm.v1.column's structural fields onto protokit's
// ColumnStructure. The referential actions are the load-bearing half: the
// foreign-key column of an embedded child relation is synthesized and carries no
// descriptor of its own, so ON DELETE / ON UPDATE cannot be recovered after the
// build and must arrive here. The rendering fields (type, unique, index, …) are
// applied later in Enrich.
func (Reader) ReadColumn(d protoreflect.FieldDescriptor) schema.ColumnStructure {
	o := columnOpts(d)
	return schema.ColumnStructure{
		Column:   o.GetColumn(),
		Skip:     o.GetSkip(),
		OnDelete: refAction(o.GetOnDelete()),
		OnUpdate: refAction(o.GetOnUpdate()),
	}
}

// --- schema.Enricher ---

// Enrich folds orm.v1's rendering options into the neutral IR: per-column
// unique/index constraints and default expressions, plus the per-database knobs
// the gorm target reads back off db.Opts.
//
// It runs after facet collection, so it reads the facets it just contributed
// rather than the descriptors — which is what lets it see options on nodes whose
// descriptor protokit never had.
//
// A column's SQL type is deliberately not folded in: it is not part of the
// neutral IR, and types.SQLForColumn resolves it from the same facet at render
// time.
func (r Reader) Enrich(ir *protokit.IR) error {
	fx := facets.New(ir)

	// Telemetry default: the telemetry plugin opt, then orm.yaml's telemetry:
	// block overrides. Metrics and logs default on when telemetry itself is on.
	telOn, telMetrics, telLogs := r.telemetry, true, true
	if r.cfg != nil && r.cfg.Telemetry != nil {
		if r.cfg.Telemetry.Enabled != nil {
			telOn = *r.cfg.Telemetry.Enabled
		}
		if r.cfg.Telemetry.Metrics != nil {
			telMetrics = *r.cfg.Telemetry.Metrics
		}
		if r.cfg.Telemetry.Logs != nil {
			telLogs = *r.cfg.Telemetry.Logs
		}
	}

	for _, db := range ir.Databases {
		for _, s := range db.Schemas {
			for _, t := range s.Tables {
				for _, c := range t.Columns {
					enrichColumn(c, fx.Column(c))
				}
			}
		}
		// Stamp the gorm target's render-time knobs onto the neutral IR. protokit
		// never interprets these; the generator's own targets read them back off
		// db.Opts.
		if db.Opts == nil {
			db.Opts = map[string]string{}
		}
		db.Opts["go_module"] = r.goModule
		db.Opts["stores"] = boolStr(r.stores)
		db.Opts["converters"] = boolStr(r.converters)
		db.Opts["telemetry"] = boolStr(telOn)
		db.Opts["telemetry_metrics"] = boolStr(telMetrics)
		db.Opts["telemetry_logs"] = boolStr(telLogs)
		db.Opts["filters"] = boolStr(r.filters)
		db.Opts["gorm_module"] = r.gormModule
		db.Opts["graphql_module"] = r.graphqlModule
	}
	return nil
}

// enrichColumn folds orm.v1.column's constraint rendering onto one column:
// unique/index are additive, and a non-empty default_value overrides the AIP enum
// default protokit may have set.
func enrichColumn(c *schema.Column, o *ormpbv1.ColumnOptions) {
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

// boolStr renders a bool as the "true"/"false" tokens db.Opts stores.
func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// indexes converts orm.v1's composite index declarations to protokit's neutral
// form.
func indexes(defs []*ormpbv1.IndexDef) []*schema.Index {
	if len(defs) == 0 {
		return nil
	}
	out := make([]*schema.Index, 0, len(defs))
	for _, d := range defs {
		out = append(out, &schema.Index{
			Name: d.GetIndex(), Columns: d.GetColumns(), Unique: d.GetUnique(),
		})
	}
	return out
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

func tableOpts(d protoreflect.MessageDescriptor) *ormpbv1.TableOptions {
	if d == nil || !proto.HasExtension(d.Options(), ormpbv1.E_Table) {
		return &ormpbv1.TableOptions{}
	}
	return proto.GetExtension(d.Options(), ormpbv1.E_Table).(*ormpbv1.TableOptions)
}

func columnOpts(d protoreflect.FieldDescriptor) *ormpbv1.ColumnOptions {
	if d == nil || !proto.HasExtension(d.Options(), ormpbv1.E_Column) {
		return &ormpbv1.ColumnOptions{}
	}
	return proto.GetExtension(d.Options(), ormpbv1.E_Column).(*ormpbv1.ColumnOptions)
}

func queryOpts(d protoreflect.FieldDescriptor) *ormpbv1.QueryOptions {
	if d == nil || !proto.HasExtension(d.Options(), ormpbv1.E_Query) {
		return &ormpbv1.QueryOptions{}
	}
	return proto.GetExtension(d.Options(), ormpbv1.E_Query).(*ormpbv1.QueryOptions)
}
