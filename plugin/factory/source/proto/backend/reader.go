// Package backend is this plugin's bridge to protokit: the reader that brings
// store.v1 into protokit's IR, and the layout policy resolved from store.yaml.
//
// protokit v1.2.0 replaced the old schema.Backend — which conflated reading a
// generator's annotations, deciding neutral names, and applying a config file —
// with separate seams. This package supplies this plugin's half of them; the
// neutral half is entity's (see [Readers], which composes the two):
//
//   - [Reader] is a schema.FacetReader over store.v1: it attaches the physical
//     storage options to each node as a facet (see factory/facets). It is also a
//     schema.StructureReader, for the narrow set of options protokit must act on
//     *while* building, and the run's schema.Enricher.
//   - [Layout] is a schema.LayoutResolver: the database/schema naming policy
//     that comes from store.yaml rather than from the protos.
//
// Keeping the annotation and the config apart is the point of the split. The
// annotation travels with the proto and must mean the same thing to every
// plugin; the config travels with the deployment and may legitimately differ.
package backend

import (
	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
	"github.com/the-protobuf-project/store/plugin/pb/storepbv1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Reader brings store.v1 into the build. It reads the vocabulary into facets,
// supplies the referential actions protokit consumes mid-build, and folds this
// plugin's render-time knobs onto each database in Enrich.
//
// It carries the plugin opts (not the layout config — that is [Layout]'s) plus
// store.yaml's telemetry block, which tunes the gorm target rather than naming
// anything.
type Reader struct {
	cfg        *Config // store.yaml; read here only for its telemetry block
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
func (Reader) Key() string { return facets.KeyStore }

// ReadFile contributes nothing: store.v1 has no file-level option. Grouping a
// file into a database is neutral structure and lives in (entity.v1.datasource).
func (Reader) ReadFile(protoreflect.FileDescriptor) (any, error) { return nil, nil }

// ReadMessage attaches the message's store.v1.table options, or nothing.
func (Reader) ReadMessage(d protoreflect.MessageDescriptor) (any, error) {
	if !hasStoreTable(d) {
		return nil, nil
	}
	return storeTableOpts(d), nil
}

// ReadField attaches the field's store.v1.column and store.v1.query options as
// one [facets.Field]. protokit stores one value per node, so a field carrying
// both annotations needs them bundled. Returns nothing when it carries neither.
func (Reader) ReadField(d protoreflect.FieldDescriptor) (any, error) {
	hasCol := hasStoreColumn(d)
	hasQuery := hasStoreQuery(d)
	if !hasCol && !hasQuery {
		return nil, nil
	}
	f := &facets.Field{}
	if hasCol {
		f.Column = storeColumnOpts(d)
	}
	if hasQuery {
		f.Query = storeQueryOpts(d)
	}
	return f, nil
}

// --- schema.StructureReader ---

// ReadDatasource contributes nothing: every field of the old datasource option
// was neutral structure and now lives in (entity.v1.datasource), which
// entity.Reader supplies.
func (Reader) ReadDatasource(protoreflect.FileDescriptor) schema.Datasource {
	return schema.Datasource{}
}

// ReadTable contributes nothing: name, skip, id, timestamps, and indexes are all
// neutral structure and live in (entity.v1.table).
func (Reader) ReadTable(protoreflect.MessageDescriptor) schema.TableStructure {
	return schema.TableStructure{}
}

// ReadColumn supplies only the referential actions — the one thing store.v1
// expresses that protokit consumes while building rather than reads afterward.
// The foreign-key column of an embedded child relation is synthesized and
// carries no descriptor of its own, so ON DELETE / ON UPDATE cannot be recovered
// from a facet after the fact and must arrive here.
//
// Name and skip are absent on purpose: they are entity.v1's.
func (Reader) ReadColumn(d protoreflect.FieldDescriptor) schema.ColumnStructure {
	o := storeColumnOpts(d)
	return schema.ColumnStructure{
		OnDelete: refAction(o.GetOnDelete()),
		OnUpdate: refAction(o.GetOnUpdate()),
	}
}

// --- schema.Enricher ---

// Enrich folds the physical rendering options into the neutral IR: per-column
// unique/index constraints and default expressions, plus the per-database knobs
// the gorm target reads back off db.Opts.
//
// It runs after facet collection, so it reads through [facets.Set] rather than
// off the descriptors — which is what lets a synthesized column, which has no
// descriptor, resolve at all.
//
// A column's SQL type is not folded in: it is not part of the neutral IR, and
// types.SQLForColumn resolves it from the same facet at render time.
func (r Reader) Enrich(ir *protokit.IR) error {
	fx := facets.New(ir)

	// Telemetry default: the telemetry plugin opt, then store.yaml's telemetry:
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

	// Outbox companions are synthesized before the column pass so they are
	// enriched like any other table, and before index finalization so their
	// declared index is named and ordered alongside every other one.
	appendOutboxTables(ir, fx)

	for _, db := range ir.Databases {
		for _, s := range db.Schemas {
			for _, t := range s.Tables {
				for _, c := range t.Columns {
					enrichColumn(t, c, fx.Column(c))
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

// enrichColumn folds store.v1.column's constraint rendering onto one column:
// unique/index are additive, and a non-empty default_value overrides the AIP enum
// default protokit may have set.
func enrichColumn(t *schema.Table, c *schema.Column, o *storepbv1.ColumnOptions) {
	if v := o.GetDefaultValue(); v != "" {
		c.Default = v
	}
	if o.GetUnique() {
		c.Unique = true
	}
	if o.GetIndex() {
		c.Index = true
		indexColumn(t, c)
	}
}

// indexColumn records a column-level index request as a real single-column entry
// in t.Indexes. Column.Index alone only ever reached the GORM struct tag, so an
// index declared that way existed for AutoMigrate users and silently not for
// anyone applying the SQL or Prisma output. Running during enrichment puts the
// index in place before protokit's finalizeIndexes, so it is auto-named and
// suppresses the redundant foreign-key index the same way a declared composite
// does.
//
// Redundant requests are dropped rather than emitted: PostgreSQL already indexes
// primary-key and unique columns, and a B-tree serves its leftmost prefix, so a
// composite index starting with this column covers single-column lookups too.
// Emitting them anyway would cost writes and storage for no read benefit — and,
// under AutoMigrate, one extra catalog round trip per index on every boot.
func indexColumn(t *schema.Table, c *schema.Column) {
	if c.PrimaryKey || c.Unique {
		return
	}
	for _, idx := range t.Indexes {
		if len(idx.Columns) > 0 && idx.Columns[0] == c.Name {
			return
		}
	}
	t.Indexes = append(t.Indexes, &schema.Index{Columns: []string{c.Name}})
}

// boolStr renders a bool as the "true"/"false" tokens db.Opts stores.
func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// refAction converts a store.v1.ReferentialAction to its SQL clause form.
func refAction(a storepbv1.ReferentialAction) string {
	switch a {
	case storepbv1.ReferentialAction_REFERENTIAL_ACTION_CASCADE:
		return "CASCADE"
	case storepbv1.ReferentialAction_REFERENTIAL_ACTION_RESTRICT:
		return "RESTRICT"
	case storepbv1.ReferentialAction_REFERENTIAL_ACTION_SET_NULL:
		return "SET NULL"
	case storepbv1.ReferentialAction_REFERENTIAL_ACTION_SET_DEFAULT:
		return "SET DEFAULT"
	case storepbv1.ReferentialAction_REFERENTIAL_ACTION_NO_ACTION:
		return "NO ACTION"
	default:
		return ""
	}
}

// --- option accessors (safe empty value when the extension or descriptor is absent) ---

func hasStoreTable(d protoreflect.MessageDescriptor) bool {
	return d != nil && proto.HasExtension(d.Options(), storepbv1.E_Table)
}

func hasStoreColumn(d protoreflect.FieldDescriptor) bool {
	return d != nil && proto.HasExtension(d.Options(), storepbv1.E_Column)
}

func hasStoreQuery(d protoreflect.FieldDescriptor) bool {
	return d != nil && proto.HasExtension(d.Options(), storepbv1.E_Query)
}

func storeTableOpts(d protoreflect.MessageDescriptor) *storepbv1.TableOptions {
	if d == nil || !proto.HasExtension(d.Options(), storepbv1.E_Table) {
		return &storepbv1.TableOptions{}
	}
	return proto.GetExtension(d.Options(), storepbv1.E_Table).(*storepbv1.TableOptions)
}

func storeColumnOpts(d protoreflect.FieldDescriptor) *storepbv1.ColumnOptions {
	if d == nil || !proto.HasExtension(d.Options(), storepbv1.E_Column) {
		return &storepbv1.ColumnOptions{}
	}
	return proto.GetExtension(d.Options(), storepbv1.E_Column).(*storepbv1.ColumnOptions)
}

func storeQueryOpts(d protoreflect.FieldDescriptor) *storepbv1.QueryOptions {
	if d == nil || !proto.HasExtension(d.Options(), storepbv1.E_Query) {
		return &storepbv1.QueryOptions{}
	}
	return proto.GetExtension(d.Options(), storepbv1.E_Query).(*storepbv1.QueryOptions)
}
