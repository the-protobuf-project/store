// Package telemetry owns everything this repository knows about telemetry.v1:
// reading the annotations, resolving what each table's instrumentation should
// be, and rendering the generated adapter that talks to the SDK.
//
// It is deliberately a top-level package with no dependency on any target. The
// eventual protoc-gen-telemetry is meant to be a separate binary, and the way to
// keep that a realistic prospect is to make the split a `git mv` plus a go.mod —
// not an archaeology exercise through a target that grew telemetry conditionals
// in a dozen places. Nothing outside this package may read a telemetry.v1
// option; a target asks [Set] instead.
//
// What it does not own: where the instrumentation goes. Weaving a span into a
// generated GORM store body is the GORM target's rendering of its own store, and
// belongs there. This package answers "is this table instrumented, under what
// span name, with metrics?" and renders the SDK adapter package; the target
// decides what to do with the answer.
package telemetry

import (
	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/telemetry/telemetry-go/protobuf/telemetry/v1/telemetrypbv1"
)

const (
	// Key namespaces this reader's facets. It names the vocabulary, not the
	// plugin that happens to register it today.
	Key = "telemetry.v1"

	// Module is the first-party observability SDK the generated adapter imports.
	// The plugin itself never imports it — only generated consumers do.
	Module = "github.com/the-protobuf-project/telemetry/telemetry-go"

	// Package is the name and output directory of the generated adapter, emitted
	// at <go_module>/telemetry.
	Package = "telemetry"
)

// Field is the facet attached to a field: its telemetry.v1 field options.
type Field = *telemetrypbv1.TelemetryFieldOptions

// Message is the facet attached to a message: its telemetry.v1 table options.
type Message = *telemetrypbv1.TelemetryOptions

// Reader brings telemetry.v1 into the build as facets.
//
// It is only a FacetReader: telemetry decides nothing about what anything is
// *called*, so it supplies no structure, and it stamps no per-database settings,
// so it enriches nothing. That narrowness is the point — it is the shape a
// reader has when it can be lifted into its own binary unchanged.
type Reader struct{}

var _ schema.FacetReader = Reader{}

// NewReader returns the telemetry.v1 facet reader.
func NewReader() Reader { return Reader{} }

// Key namespaces this reader's facets.
func (Reader) Key() string { return Key }

// ReadFile contributes nothing: telemetry.v1 has no file-level option.
func (Reader) ReadFile(protoreflect.FileDescriptor) (any, error) { return nil, nil }

// ReadMessage attaches the message's (telemetry.v1.telemetry) options.
func (Reader) ReadMessage(d protoreflect.MessageDescriptor) (any, error) {
	if d == nil || !proto.HasExtension(d.Options(), telemetrypbv1.E_Telemetry) {
		return nil, nil
	}
	return proto.GetExtension(d.Options(), telemetrypbv1.E_Telemetry).(Message), nil
}

// ReadField attaches the field's (telemetry.v1.telemetry_field) options.
func (Reader) ReadField(d protoreflect.FieldDescriptor) (any, error) {
	if d == nil || !proto.HasExtension(d.Options(), telemetrypbv1.E_TelemetryField) {
		return nil, nil
	}
	return proto.GetExtension(d.Options(), telemetrypbv1.E_TelemetryField).(Field), nil
}

// --- per-database settings ---
//
// These live on Database.Opts, stamped by whichever plugin resolved the
// telemetry opt and its config block. Reading them here rather than in the
// target keeps the key names this package's business.

// Enabled reports whether to fold instrumentation into db's generated output.
func Enabled(db *schema.Database) bool { return db.Opt("telemetry") == "true" }

// Metrics reports whether instrumented code records per-operation metrics in
// addition to spans. Only meaningful when [Enabled]; a per-table
// (telemetry.v1.telemetry).metrics narrows it further.
func Metrics(db *schema.Database) bool { return db.Opt("telemetry_metrics") == "true" }

// Logs reports whether the adapter logs failed operations. Only meaningful when
// [Enabled].
func Logs(db *schema.Database) bool { return db.Opt("telemetry_logs") == "true" }

// --- resolution ---

// Resolution is one table's effective instrumentation.
type Resolution struct {
	// Enabled is false when the tree-wide opt is off or the table opted out.
	Enabled bool

	// Metrics is false when the tree or the table turned metrics off. Always
	// false when Enabled is false.
	Metrics bool

	// SpanPrefix names the table's spans, e.g. "bookstore_v1.Book/Create".
	SpanPrefix string
}

// Set is the per-run telemetry lookup a target resolves from the IR once.
//
// The zero Set reports every table uninstrumented, which is what a caller with
// no IR (the databases-only Generate path) needs.
type Set struct{ ir *protokit.IR }

// New returns the Set reading ir's telemetry.v1 facets.
func New(ir *protokit.IR) Set { return Set{ir: ir} }

// Table resolves one table's instrumentation: the tree-wide opt gates
// everything, (telemetry.v1.telemetry).enabled can opt a table out, metrics
// narrows further, and the span prefix defaults to "<schema>.<Model>".
func (s Set) Table(db *schema.Database, sc *schema.Schema, t *schema.Table) Resolution {
	if !Enabled(db) {
		return Resolution{}
	}
	o := s.message(t)
	// Presence, not truth: these are optional bools, and an unset field means
	// "inherit the tree-wide setting" rather than false.
	if o.Enabled != nil && !o.GetEnabled() {
		return Resolution{}
	}
	r := Resolution{Enabled: true, Metrics: Metrics(db)}
	if o.Metrics != nil {
		r.Metrics = o.GetMetrics()
	}
	r.SpanPrefix = o.GetSpanPrefix()
	if r.SpanPrefix == "" {
		r.SpanPrefix = sc.Name + "." + t.LocalName
	}
	return r
}

// FieldTag renders a column's telemetry struct tag — a "trace:<name>"
// directive the SDK lifts into a span attribute on traced writes. Empty when the
// table is not instrumented or the field is not labelled.
func (s Set) FieldTag(enabled bool, t *schema.Table, col *schema.Column) string {
	if !enabled {
		return ""
	}
	o := s.field(col)
	if !o.GetLabel() {
		return ""
	}
	return `telemetry:"trace:` + attrName(t, col, o.GetLabelKey()) + `"`
}

// attrName is the span-attribute name for a field: the explicit override, or
// "<model_snake>.<column>" (e.g. "book.genre").
func attrName(t *schema.Table, col *schema.Column, override string) string {
	if override != "" {
		return override
	}
	return naming.SnakeCase(t.LocalName) + "." + col.Name
}

// message returns a table's telemetry facet, never nil.
func (s Set) message(t *schema.Table) Message {
	if t != nil {
		if o, ok := protokit.Facet[Message](s.ir, Key, t.Node); ok && o != nil {
			return o
		}
	}
	return &telemetrypbv1.TelemetryOptions{}
}

// field returns a column's telemetry facet, never nil.
func (s Set) field(col *schema.Column) Field {
	if col != nil {
		if o, ok := protokit.Facet[Field](s.ir, Key, col.Node); ok && o != nil {
			return o
		}
	}
	return &telemetrypbv1.TelemetryFieldOptions{}
}
