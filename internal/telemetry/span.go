package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// WithSpan wraps fn in a span named name, started on tracer under ctx. On
// failure the returned error is recorded on the span as an exception event
// and the span status is set to Error — the contract downstream git and
// store calls (criterion 5) must satisfy. On success the span status is set
// to Ok. The original error (or nil) is returned unchanged; WithSpan never
// alters fn's outcome, only observes it.
func WithSpan(ctx context.Context, tracer trace.Tracer, name string, fn func(context.Context) error) error {
	ctx, span := tracer.Start(ctx, name)
	defer span.End()

	err := fn(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	span.SetStatus(codes.Ok, "")
	return nil
}
