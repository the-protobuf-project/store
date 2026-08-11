{{.Header}}

// Package telemetry is the only generated package that imports the
// opentelementry SDK: it adapts an *opentelementry.Opentelementry handle into
// the store-level gormx.Telemetry seam and provides a SQL-level gorm plugin,
// so an application wires observability once and every generated store and
// query goes through it.
package {{.Package}}

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"time"

	"{{.OpentelementryImport}}"
	"gorm.io/gorm"
{{- if .Stores}}
	"{{.GormxImport}}"
{{- end}}
)

{{if .Stores}}
// New adapts o into the gormx.Telemetry every generated store observes
// through (via WithTelemetry). A nil o is a no-op.
func New(o *opentelementry.Opentelementry) gormx.Telemetry {
	if o == nil {
		return gormx.NopTelemetry{}
	}
	return adapter{o: o}
}

type adapter struct{ o *opentelementry.Opentelementry }

// Span wraps fn in a trace span; data (the model on writes, nil elsewhere)
// carries the `opentelementry:"trace:..."`-tagged fields the SDK lifts into
// span attributes.
func (a adapter) Span(ctx context.Context, name string, data any, fn func(context.Context) error) error {
	return a.o.Tracing.Trace(ctx, name, data, func(ctx context.Context, _ *opentelementry.Span) error {
		return fn(ctx)
	})
}

// RecordOp records one completed store operation: the ops counter + duration
// histogram, attributed by table, op, and status{{if .Logs}}, and logs a
// trace-correlated error on failure (skipping a plain not-found){{end}}.
func (a adapter) RecordOp(ctx context.Context, table, op string, d time.Duration, err error) {
	status := "ok"
	if err != nil {
		status = "error"
	}
{{- if .Metrics}}
	_ = a.o.Metrics.Record(&gormx.OpMetric{Ops: 1, DurationMS: float64(d.Microseconds()) / 1000},
		opentelementry.WithAttributes(
			opentelementry.StringAttribute("table", table),
			opentelementry.StringAttribute("op", op),
			opentelementry.StringAttribute("status", status),
		))
{{- end}}
{{- if .Logs}}
	if err != nil && err != gorm.ErrRecordNotFound {
		_ = a.o.Logger.WithContext(ctx).Error("store operation failed", map[string]any{
			"table": table, "op": op, "error": err.Error(),
		})
	}
{{- end}}
}
{{end}}
// Plugin returns a gorm.Plugin that emits a span{{if .Metrics}} and a query metric{{end}}
// for every query db.Use(Plugin(o)) runs, restoring the SQL-level visibility a
// generic ORM plugin used to provide. A nil o is a no-op plugin. Wire it via
// Registry.Instrument(db, o) or directly with db.Use(telemetry.Plugin(o)).
func Plugin(o *opentelementry.Opentelementry) gorm.Plugin {
	return gormPlugin{o: o}
}

type gormPlugin struct{ o *opentelementry.Opentelementry }

func (gormPlugin) Name() string { return "telemetry" }

func (p gormPlugin) Initialize(db *gorm.DB) error {
	if p.o == nil {
		return nil
	}
	cb := db.Callback()
	hooks := []struct {
		reg  interface {
			Register(name string, fn func(*gorm.DB)) error
		}
		fn   func(*gorm.DB)
		name string
	}{
		{cb.Create().Before("gorm:create"), p.before("gorm.create"), "before:create"},
		{cb.Create().After("gorm:create"), p.after(), "after:create"},
		{cb.Query().Before("gorm:query"), p.before("gorm.query"), "before:query"},
		{cb.Query().After("gorm:query"), p.after(), "after:query"},
		{cb.Update().Before("gorm:update"), p.before("gorm.update"), "before:update"},
		{cb.Update().After("gorm:update"), p.after(), "after:update"},
		{cb.Delete().Before("gorm:delete"), p.before("gorm.delete"), "before:delete"},
		{cb.Delete().After("gorm:delete"), p.after(), "after:delete"},
		{cb.Row().Before("gorm:row"), p.before("gorm.row"), "before:row"},
		{cb.Row().After("gorm:row"), p.after(), "after:row"},
		{cb.Raw().Before("gorm:raw"), p.before("gorm.raw"), "before:raw"},
		{cb.Raw().After("gorm:raw"), p.after(), "after:raw"},
	}
	for _, h := range hooks {
		if err := h.reg.Register("telemetry:"+h.name, h.fn); err != nil {
			return err
		}
	}
	return nil
}

// spanKey and startKey stash the in-flight span and start time on the
// statement-scoped instance store, since gorm's Before/After hooks for one
// query run against the same *gorm.DB, not a shared context.
const (
	spanKey  = "telemetry:span"
	startKey = "telemetry:start"
)

func (p gormPlugin) before(name string) func(*gorm.DB) {
	return func(tx *gorm.DB) {
		ctx, span := p.o.Tracing.Start(tx.Statement.Context, name)
		tx.Statement.Context = ctx
		tx.InstanceSet(spanKey, span)
		tx.InstanceSet(startKey, time.Now())
	}
}

func (p gormPlugin) after() func(*gorm.DB) {
	return func(tx *gorm.DB) {
		v, ok := tx.InstanceGet(spanKey)
		if !ok {
			return // before never ran (e.g. a Session(SkipHooks) path)
		}
		span := v.(*opentelementry.Span)
		defer span.End()

		if tx.Statement.Table != "" {
			span.SetAttribute("db.table", tx.Statement.Table)
		}
		if tx.Statement.RowsAffected >= 0 {
			span.SetAttribute("db.rows_affected", tx.Statement.RowsAffected)
		}
		switch tx.Error {
		case nil, gorm.ErrRecordNotFound, driver.ErrSkip, io.EOF, sql.ErrNoRows:
			span.SetOK()
		default:
			span.SetError(tx.Error)
		}
{{- if .Metrics}}

		status := "ok"
		if tx.Error != nil && tx.Error != gorm.ErrRecordNotFound {
			status = "error"
		}
		start, _ := tx.InstanceGet(startKey)
		var d time.Duration
		if t, ok := start.(time.Time); ok {
			d = time.Since(t)
		}
		_ = p.o.Metrics.Record(&queryMetric{Queries: 1, DurationMS: float64(d.Microseconds()) / 1000},
			opentelementry.WithAttributes(
				opentelementry.StringAttribute("table", tx.Statement.Table),
				opentelementry.StringAttribute("status", status),
			))
{{- end}}
	}
}
{{if .Metrics}}
// queryMetric is the per-query measurement Plugin records.
type queryMetric struct {
	Queries    int64   `opentelementry:"metric:counter:orm.gorm.queries"`
	DurationMS float64 `opentelementry:"metric:histogram:orm.gorm.query_duration_ms"`
}
{{end}}
