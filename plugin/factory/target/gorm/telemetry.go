package gorm

// telemetry.go reads the telemetry.v1 telemetry options off the IR's Source
// descriptors at render time — the same pattern filterspec.go uses for query
// options — and resolves each table's effective instrumentation: the tree-wide
// telemetry opt gates everything, (telemetry.v1.telemetry) tunes one table,
// and (telemetry.v1.telemetry_field) projects a field into span attributes.
// Metrics stay DB-scoped by design (table/op/status only) — domain-level
// metrics belong to the application, not the ORM.
//
// telemetry.v1 is orm.v1's only source for these options — orm.v1 carries no
// telemetry extensions of its own (see orm/v1/annotations.proto).

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	telemetrypbv1 "github.com/the-protobuf-project/opentelementry/opentelementry-go/protobuf/telemetry/v1/telemetrypbv1"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/provenance"
)

// opentelementryModule is the import path of the first-party observability SDK
// every telemetry-generated file goes through. The plugin itself never imports
// it — only generated consumers do.
const opentelementryModule = "github.com/the-protobuf-project/opentelementry/opentelementry-go"

// telemetryPkg is the package name and output directory of the generated
// SDK adapter at <go_module>/telemetry — the only generated package that
// imports the SDK.
const telemetryPkg = "telemetry"

// tableTelemetry resolves one table's effective instrumentation: enabled only
// when the tree-wide telemetry opt is on and the table doesn't opt out via
// (telemetry.v1.telemetry).enabled=false; metrics narrows further per table;
// the span prefix defaults to "<schema>.<Model>".
func tableTelemetry(db *schema.Database, s *schema.Schema, t *schema.Table) (enabled, metrics bool, spanPrefix string) {
	if !dbTelemetry(db) {
		return false, false, ""
	}
	o := telemetryOpts(t.Source)
	if o.Enabled != nil && !o.GetEnabled() {
		return false, false, ""
	}
	metrics = dbTelemetryMetrics(db)
	if o.Metrics != nil {
		metrics = o.GetMetrics()
	}
	spanPrefix = o.GetSpanPrefix()
	if spanPrefix == "" {
		spanPrefix = s.Name + "." + t.LocalName
	}
	return true, metrics, spanPrefix
}

// telemetryTag renders a column's opentelementry struct tag — a
// "trace:<name>" directive the SDK lifts into a span attribute on traced
// writes. Empty when the table isn't instrumented or the field isn't labeled.
func telemetryTag(enabled bool, t *schema.Table, col *schema.Column) string {
	if !enabled {
		return ""
	}
	o := telemetryFieldOpts(col.Source)
	if !o.GetLabel() {
		return ""
	}
	return `opentelementry:"trace:` + telemetryAttrName(t, col, o.GetLabelKey()) + `"`
}

// telemetryAttrName is the span-attribute name for a field: the explicit
// override, or "<model_snake>.<column>" (e.g. "book.genre").
func telemetryAttrName(t *schema.Table, col *schema.Column, override string) string {
	if override != "" {
		return override
	}
	return naming.SnakeCase(t.LocalName) + "." + col.Name
}

// telemetryPkgView assembles the data for the once-per-tree telemetry
// package: the SDK adapter (with stores) and the gorm query plugin.
func telemetryPkgView(db *schema.Database) map[string]any {
	return map[string]any{
		"Header": provenance.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Database:      db.Name,
			SchemaLabel:   "package",
			Schema:        telemetryPkg,
			Notes:         []string{"First-party opentelementry adapter: the stores' gormx.Telemetry and the SQL-level gorm plugin."},
		}, opentelementryModule),
		"Package":              telemetryPkg,
		"Stores":               dbStores(db),
		"Metrics":              dbTelemetryMetrics(db),
		"Logs":                 dbTelemetryLogs(db),
		"OpentelementryImport": opentelementryModule,
		"GormxImport":          dbGoModule(db) + "/" + gormxPkg,
	}
}

// --- option accessors (safe empty value when the extension or descriptor is absent) ---

func telemetryOpts(d protoreflect.MessageDescriptor) *telemetrypbv1.TelemetryOptions {
	if d == nil || !proto.HasExtension(d.Options(), telemetrypbv1.E_Telemetry) {
		return &telemetrypbv1.TelemetryOptions{}
	}
	return proto.GetExtension(d.Options(), telemetrypbv1.E_Telemetry).(*telemetrypbv1.TelemetryOptions)
}

func telemetryFieldOpts(d protoreflect.FieldDescriptor) *telemetrypbv1.TelemetryFieldOptions {
	if d == nil || !proto.HasExtension(d.Options(), telemetrypbv1.E_TelemetryField) {
		return &telemetrypbv1.TelemetryFieldOptions{}
	}
	return proto.GetExtension(d.Options(), telemetrypbv1.E_TelemetryField).(*telemetrypbv1.TelemetryFieldOptions)
}
