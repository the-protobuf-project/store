{{.Header}}

package filterx

import (
	"context"

	"{{.OpentelementryImport}}"
)

// OpentelementryObserver adapts an *opentelementry.Opentelementry handle to
// the Observer interface the list engines report through: one span per query
// and a debug log per rejected filter/order input. Wire it when building an
// engine:
//
//	filterx.Gorm[Model](spec).Observe(filterx.OpentelementryObserver(o))
func OpentelementryObserver(o *opentelementry.Opentelementry) Observer {
	if o == nil {
		return NopObserver{}
	}
	return opentelementryObserver{o: o}
}

type opentelementryObserver struct{ o *opentelementry.Opentelementry }

// Span wraps fn in a trace span.
func (ob opentelementryObserver) Span(ctx context.Context, name string, fn func(context.Context) error) error {
	return ob.o.Tracing.Trace(ctx, name, nil, func(ctx context.Context, _ *opentelementry.Span) error {
		return fn(ctx)
	})
}

// Debug logs a non-fatal engine event.
func (ob opentelementryObserver) Debug(msg string, kv map[string]any) {
	ob.o.Logger.Debug(msg, kv)
}
