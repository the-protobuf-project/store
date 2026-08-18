{{.Header}}

package filterx

import (
	"context"

	"{{.TelemetrySDKImport}}"
)

// TelemetryObserver adapts an *telemetry.Telemetry handle to
// the Observer interface the list engines report through: one span per query
// and a debug log per rejected filter/order input. Wire it when building an
// engine:
//
//	filterx.Gorm[Model](spec).Observe(filterx.TelemetryObserver(o))
func TelemetryObserver(o *telemetry.Telemetry) Observer {
	if o == nil {
		return NopObserver{}
	}
	return telemetryObserver{o: o}
}

type telemetryObserver struct{ o *telemetry.Telemetry }

// Span wraps fn in a trace span.
func (ob telemetryObserver) Span(ctx context.Context, name string, fn func(context.Context) error) error {
	return ob.o.Tracing.Trace(ctx, name, nil, func(ctx context.Context, _ *telemetry.Span) error {
		return fn(ctx)
	})
}

// Debug logs a non-fatal engine event.
func (ob telemetryObserver) Debug(msg string, kv map[string]any) {
	ob.o.Logger.Debug(msg, kv)
}
